/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scheduling

import (
	"fmt"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/api/scheduling/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	_ "github.com/stretchr/testify/assert"
)

var _ = SIGDescribe("SchedulerPreemption [Serial]", func() {
	var cs clientset.Interface
	var nodeList *v1.NodeList
	var systemPodsNo int
	var ns string
	f := framework.NewDefaultFramework("sched-preemption")
	ignoreLabels := framework.ImagePullerLabels

	lowPriority, highPriority := int32(100), int32(1000)
	lowPriorityClassName := f.BaseName + "-low-priority"
	highPriorityClassName := f.BaseName + "-high-priority"

	AfterEach(func() {
	})

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace.Name
		nodeList = &v1.NodeList{}

		utilfeature.DefaultFeatureGate.Set(fmt.Sprintf("%s=true", features.PodPriority))
		pcs, err := cs.SchedulingV1alpha1().PriorityClasses().List(metav1.ListOptions{})
		framework.ExpectNoError(err)
		if len(pcs.Items) == 0 {
			_, err := f.ClientSet.SchedulingV1alpha1().PriorityClasses().Create(&v1alpha1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: highPriorityClassName}, Value: highPriority})
			framework.ExpectNoError(err)
			_, err = f.ClientSet.SchedulingV1alpha1().PriorityClasses().Create(&v1alpha1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: lowPriorityClassName}, Value: lowPriority})
			framework.ExpectNoError(err)
		}

		framework.WaitForAllNodesHealthy(cs, time.Minute)
		masterNodes, nodeList = framework.GetMasterAndWorkerNodesOrDie(cs)

		err = framework.CheckTestingNSDeletedExcept(cs, ns)
		framework.ExpectNoError(err)

		// Every test case in this suite assumes that cluster add-on pods stay stable and
		// cannot be run in parallel with any other test that touches Nodes or Pods.
		// It is so because we need to have precise control on what's running in the cluster.
		systemPods, err := framework.GetPodsInNamespace(cs, ns, ignoreLabels)
		Expect(err).NotTo(HaveOccurred())
		systemPodsNo = 0
		for _, pod := range systemPods {
			if !masterNodes.Has(pod.Spec.NodeName) && pod.DeletionTimestamp == nil {
				systemPodsNo++
			}
		}
		err = framework.WaitForPodsRunningReady(cs, metav1.NamespaceSystem, int32(systemPodsNo), 0, framework.PodReadyBeforeTimeout, ignoreLabels)
		Expect(err).NotTo(HaveOccurred())

		err = framework.WaitForPodsSuccess(cs, metav1.NamespaceSystem, framework.ImagePullerLabels, framework.ImagePrePullingTimeout)
		Expect(err).NotTo(HaveOccurred())

		for _, node := range nodeList.Items {
			framework.Logf("\nLogging pods the kubelet thinks is on node %v before test", node.Name)
			framework.PrintAllKubeletPods(cs, node.Name)
		}

	})

	// This test verifies that when a higher priority pod is created and no node with
	// enough resources is found, scheduler preempts a lower priroity pod to schedule
	// the high priority pod.
	It("validates basic preemption works", func() {

		var cpuAllocatable, memAllocatable resource.Quantity
		// Create one pod per node that uses almost all of the node's resources.
		for _, node := range nodeList.Items {
			cpuAllocatable, found := node.Status.Allocatable["cpu"]
			Expect(found).To(Equal(true))
			//cpuAllocatableMil := cpuAllocatable.MilliValue()
			memAllocatable, found := node.Status.Allocatable["memory"]
			Expect(found).To(Equal(true))
			//memAllocatableVal := memAllocatable.Value()
			// Now run a higher priority pod and make sure that the pod is scheduled.
			createPausePod(f, pausePodConfig{
				Name:              node.Name + "-pod",
				PriorityClassName: lowPriorityClassName,
				Resources: &v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceCPU:    cpuAllocatable,
						v1.ResourceMemory: memAllocatable,
					},
				},
			})
			framework.Logf("Node: %v", node)
		}

		currentlyScheduledPods := framework.WaitForStableCluster(cs, masterNodes)
		Expect(currentlyScheduledPods).To(Equal(len(nodeList.Items)))
		// Create a high priority pod and make sure it is scheduled.
		runPausePod(f, pausePodConfig{
			Name:              "preemptor-pod",
			PriorityClassName: highPriorityClassName,
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU:    cpuAllocatable,
					v1.ResourceMemory: memAllocatable,
				},
			},
		})
	})

})
