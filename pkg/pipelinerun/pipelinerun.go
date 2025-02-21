// Copyright © 2019 The Tekton Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipelinerun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tektoncd/cli/pkg/actions"
	"github.com/tektoncd/cli/pkg/cli"
	"github.com/tektoncd/cli/pkg/formatted"
	prsort "github.com/tektoncd/cli/pkg/pipelinerun/sort"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var prGroupResource = schema.GroupVersionResource{Group: "tekton.dev", Resource: "pipelineruns"}

// GetAllPipelineRuns returns all pipelinesruns running in a namespace
func GetAllPipelineRuns(p cli.Params, opts metav1.ListOptions, limit int) ([]string, error) {
	cs, err := p.Clients()
	if err != nil {
		return nil, err
	}

	runs, err := List(cs, opts, p.Namespace())
	if err != nil {
		return nil, err
	}

	runslen := len(runs.Items)
	if runslen > 1 {
		prsort.SortByStartTime(runs.Items)
	}

	if limit > runslen {
		limit = runslen
	}

	ret := []string{}
	for i, run := range runs.Items {
		if i < limit {
			ret = append(ret, run.ObjectMeta.Name+" started "+formatted.Age(run.Status.StartTime, p.Time()))
		}
	}
	return ret, nil
}

func List(c *cli.Clients, opts metav1.ListOptions, ns string) (*v1beta1.PipelineRunList, error) {
	unstructuredPR, err := actions.List(prGroupResource, c.Dynamic, c.Tekton.Discovery(), ns, opts)
	if err != nil {
		return nil, err
	}

	var prList *v1beta1.PipelineRunList
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPR.UnstructuredContent(), &prList); err != nil {
		return nil, err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list pipelineruns from %s namespace \n", ns)
		return nil, err
	}

	var populatedPRs []v1beta1.PipelineRun

	for _, pr := range prList.Items {
		updatedPR, err := populatePipelineRunTaskStatuses(c, ns, pr)
		if err != nil {
			return nil, err
		}
		populatedPRs = append(populatedPRs, *updatedPR)
	}

	prList.Items = populatedPRs

	return prList, nil
}

// It will fetch the resource based on the api available and return v1beta1 form
func Get(c *cli.Clients, prname string, opts metav1.GetOptions, ns string) (*v1beta1.PipelineRun, error) {
	gvr, err := actions.GetGroupVersionResource(prGroupResource, c.Tekton.Discovery())
	if err != nil {
		return nil, err
	}

	if gvr.Version == "v1alpha1" {
		pipelinerun, err := getV1alpha1(c, prname, opts, ns)
		if err != nil {
			return nil, err
		}
		var pipelinerunConverted v1beta1.PipelineRun
		err = pipelinerun.ConvertTo(context.Background(), &pipelinerunConverted)
		if err != nil {
			return nil, err
		}
		return &pipelinerunConverted, nil
	}
	return GetV1beta1(c, prname, opts, ns)
}

// It will fetch the resource in v1beta1 struct format
func GetV1beta1(c *cli.Clients, prname string, opts metav1.GetOptions, ns string) (*v1beta1.PipelineRun, error) {
	unstructuredPR, err := actions.Get(prGroupResource, c.Dynamic, c.Tekton.Discovery(), prname, ns, opts)
	if err != nil {
		return nil, err
	}

	var pipelinerun *v1beta1.PipelineRun
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPR.UnstructuredContent(), &pipelinerun); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get pipelinerun from %s namespace \n", ns)
		return nil, err
	}

	populatedPR, err := populatePipelineRunTaskStatuses(c, ns, *pipelinerun)
	if err != nil {
		return nil, err
	}

	return populatedPR, nil
}

// It will fetch the resource in v1alpha1 struct format
func getV1alpha1(c *cli.Clients, prname string, opts metav1.GetOptions, ns string) (*v1alpha1.PipelineRun, error) {
	unstructuredPR, err := actions.Get(prGroupResource, c.Dynamic, c.Tekton.Discovery(), prname, ns, opts)
	if err != nil {
		return nil, err
	}

	var pipelinerun *v1alpha1.PipelineRun
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPR.UnstructuredContent(), &pipelinerun); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get pipelinerun from %s namespace \n", ns)
		return nil, err
	}
	return pipelinerun, nil
}

func Watch(c *cli.Clients, opts metav1.ListOptions, ns string) (watch.Interface, error) {
	watch, err := actions.Watch(prGroupResource, c, ns, opts)
	if err != nil {
		return nil, err
	}
	return watch, nil
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

func Cancel(c *cli.Clients, prname string, opts metav1.PatchOptions, cancelStatus, ns string) (*v1beta1.PipelineRun, error) {
	payload := []patchStringValue{{
		Op:    "replace",
		Path:  "/spec/status",
		Value: cancelStatus,
	}}

	data, _ := json.Marshal(payload)
	prGroupResource := schema.GroupVersionResource{Group: "tekton.dev", Resource: "pipelineruns"}
	unstructuredPR, err := actions.Patch(prGroupResource, c, prname, data, opts, ns)
	if err != nil {
		return nil, err
	}

	var pipelinerun *v1beta1.PipelineRun
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPR.UnstructuredContent(), &pipelinerun); err != nil {
		return nil, err
	}

	return pipelinerun, nil
}

// It will create the resource based on the api available.
func Create(c *cli.Clients, pr *v1beta1.PipelineRun, opts metav1.CreateOptions, ns string) (*v1beta1.PipelineRun, error) {
	gvr, err := actions.GetGroupVersionResource(prGroupResource, c.Tekton.Discovery())
	if err != nil {
		return nil, err
	}

	if gvr.Version == "v1alpha1" {
		var pipelinerunConverted v1alpha1.PipelineRun
		err = pipelinerunConverted.ConvertFrom(context.Background(), pr)
		if err != nil {
			return nil, err
		}
		pipelinerunConverted.Kind = "PipelineRun"
		pipelinerunConverted.APIVersion = "tekton.dev/v1alpha1"
		return createUnstructured(&pipelinerunConverted, c, opts, ns, gvr)
	}
	return createUnstructured(pr, c, opts, ns, gvr)
}

func createUnstructured(obj runtime.Object, c *cli.Clients, opts metav1.CreateOptions, ns string, resource *schema.GroupVersionResource) (*v1beta1.PipelineRun, error) {
	object, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	unstructuredPR := &unstructured.Unstructured{
		Object: object,
	}

	newUnstructuredPR, err := actions.Create(*resource, c, unstructuredPR, ns, opts)
	if err != nil {
		return nil, err
	}

	var pipelinerun *v1beta1.PipelineRun
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(newUnstructuredPR.UnstructuredContent(), &pipelinerun); err != nil {
		return nil, err
	}

	return pipelinerun, nil
}

func populatePipelineRunTaskStatuses(c *cli.Clients, ns string, pr v1beta1.PipelineRun) (*v1beta1.PipelineRun, error) {
	taskRunMap, runMap, err := getFullPipelineTaskStatuses(context.Background(), c.Tekton, ns, &pr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get TaskRun and Run statuses for PipelineRun %s from namespace %s\n", pr.Name, ns)
		return nil, err
	}
	pr.Status.TaskRuns = taskRunMap
	pr.Status.Runs = runMap

	return &pr, nil
}

// getFullPipelineTaskStatuses returns populated TaskRun and Run status maps for a PipelineRun from its ChildReferences.
// If the PipelineRun has no ChildReferences, its .Status.TaskRuns and .Status.Runs will be returned instead.
// TODO(abayer): Remove in favor of github.com/tektoncd/pipeline/pkg/status.GetFullPipelineTaskStatuses when CLI can move to Pipeline v0.36.0 or later.
func getFullPipelineTaskStatuses(ctx context.Context, client versioned.Interface, ns string, pr *v1beta1.PipelineRun) (map[string]*v1beta1.PipelineRunTaskRunStatus,
	map[string]*v1beta1.PipelineRunRunStatus, error) {
	// If the PipelineRun is nil, just return
	if pr == nil {
		return nil, nil, nil
	}

	// If there are no child references or either TaskRuns or Runs is non-zero, return the existing TaskRuns and Runs maps
	if len(pr.Status.ChildReferences) == 0 || len(pr.Status.TaskRuns) > 0 || len(pr.Status.Runs) > 0 {
		return pr.Status.TaskRuns, pr.Status.Runs, nil
	}

	trStatuses := make(map[string]*v1beta1.PipelineRunTaskRunStatus)
	runStatuses := make(map[string]*v1beta1.PipelineRunRunStatus)

	for _, cr := range pr.Status.ChildReferences {
		switch cr.Kind {
		case "TaskRun":
			tr, err := client.TektonV1beta1().TaskRuns(ns).Get(ctx, cr.Name, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return nil, nil, err
			}

			trStatuses[cr.Name] = &v1beta1.PipelineRunTaskRunStatus{
				PipelineTaskName: cr.PipelineTaskName,
				WhenExpressions:  cr.WhenExpressions,
			}

			if tr != nil {
				trStatuses[cr.Name].Status = &tr.Status
			}
		case "Run":
			r, err := client.TektonV1alpha1().Runs(ns).Get(ctx, cr.Name, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return nil, nil, err
			}

			runStatuses[cr.Name] = &v1beta1.PipelineRunRunStatus{
				PipelineTaskName: cr.PipelineTaskName,
				WhenExpressions:  cr.WhenExpressions,
			}

			if r != nil {
				runStatuses[cr.Name].Status = &r.Status
			}
		default:
			// Don't do anything for unknown types.
		}
	}

	return trStatuses, runStatuses, nil
}
