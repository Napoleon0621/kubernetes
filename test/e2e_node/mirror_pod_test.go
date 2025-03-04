/*
Copyright 2016 The Kubernetes Authors.

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

package e2enode

import (
	"context"
	goerrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"
	admissionapi "k8s.io/pod-security-admission/api"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = SIGDescribe("MirrorPod", func() {
	f := framework.NewDefaultFramework("mirror-pod")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelBaseline
	ginkgo.Context("when create a mirror pod ", func() {
		var ns, podPath, staticPodName, mirrorPodName string
		ginkgo.BeforeEach(func(ctx context.Context) {
			ns = f.Namespace.Name
			staticPodName = "static-pod-" + string(uuid.NewUUID())
			mirrorPodName = staticPodName + "-" + framework.TestContext.NodeName

			podPath = framework.TestContext.KubeletConfig.StaticPodPath

			ginkgo.By("create the static pod")
			err := createStaticPod(podPath, staticPodName, ns,
				imageutils.GetE2EImage(imageutils.Nginx), v1.RestartPolicyAlways)
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to be running")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRunning(ctx, f.ClientSet, mirrorPodName, ns)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())
		})
		/*
			Release: v1.9
			Testname: Mirror Pod, update
			Description: Updating a static Pod MUST recreate an updated mirror Pod. Create a static pod, verify that a mirror pod is created. Update the static pod by changing the container image, the mirror pod MUST be re-created and updated with the new image.
		*/
		ginkgo.It("should be updated when static pod updated [NodeConformance]", func(ctx context.Context) {
			ginkgo.By("get mirror pod uid")
			pod, err := f.ClientSet.CoreV1().Pods(ns).Get(ctx, mirrorPodName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			uid := pod.UID

			ginkgo.By("update the static pod container image")
			image := imageutils.GetPauseImageName()
			err = createStaticPod(podPath, staticPodName, ns, image, v1.RestartPolicyAlways)
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to be updated")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRecreatedAndRunning(ctx, f.ClientSet, mirrorPodName, ns, uid)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())

			ginkgo.By("check the mirror pod container image is updated")
			pod, err = f.ClientSet.CoreV1().Pods(ns).Get(ctx, mirrorPodName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			framework.ExpectEqual(len(pod.Spec.Containers), 1)
			framework.ExpectEqual(pod.Spec.Containers[0].Image, image)
		})
		/*
			Release: v1.9
			Testname: Mirror Pod, delete
			Description:  When a mirror-Pod is deleted then the mirror pod MUST be re-created. Create a static pod, verify that a mirror pod is created. Delete the mirror pod, the mirror pod MUST be re-created and running.
		*/
		ginkgo.It("should be recreated when mirror pod gracefully deleted [NodeConformance]", func(ctx context.Context) {
			ginkgo.By("get mirror pod uid")
			pod, err := f.ClientSet.CoreV1().Pods(ns).Get(ctx, mirrorPodName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			uid := pod.UID

			ginkgo.By("delete the mirror pod with grace period 30s")
			err = f.ClientSet.CoreV1().Pods(ns).Delete(ctx, mirrorPodName, *metav1.NewDeleteOptions(30))
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to be recreated")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRecreatedAndRunning(ctx, f.ClientSet, mirrorPodName, ns, uid)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())
		})
		/*
			Release: v1.9
			Testname: Mirror Pod, force delete
			Description: When a mirror-Pod is deleted, forcibly, then the mirror pod MUST be re-created. Create a static pod, verify that a mirror pod is created. Delete the mirror pod with delete wait time set to zero forcing immediate deletion, the mirror pod MUST be re-created and running.
		*/
		ginkgo.It("should be recreated when mirror pod forcibly deleted [NodeConformance]", func(ctx context.Context) {
			ginkgo.By("get mirror pod uid")
			pod, err := f.ClientSet.CoreV1().Pods(ns).Get(ctx, mirrorPodName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			uid := pod.UID

			ginkgo.By("delete the mirror pod with grace period 0s")
			err = f.ClientSet.CoreV1().Pods(ns).Delete(ctx, mirrorPodName, *metav1.NewDeleteOptions(0))
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to be recreated")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRecreatedAndRunning(ctx, f.ClientSet, mirrorPodName, ns, uid)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())
		})
		ginkgo.AfterEach(func(ctx context.Context) {
			ginkgo.By("delete the static pod")
			err := deleteStaticPod(podPath, staticPodName, ns)
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to disappear")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodDisappear(ctx, f.ClientSet, mirrorPodName, ns)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())
		})
	})
	ginkgo.Context("when create a mirror pod without changes ", func() {
		var ns, podPath, staticPodName, mirrorPodName string
		ginkgo.BeforeEach(func() {
		})
		/*
			Release: v1.23
			Testname: Mirror Pod, recreate
			Description: When a static pod's manifest is removed and readded, the mirror pod MUST successfully recreate. Create the static pod, verify it is running, remove its manifest and then add it back, and verify the static pod runs again.
		*/
		ginkgo.It("should successfully recreate when file is removed and recreated [NodeConformance]", func(ctx context.Context) {
			ns = f.Namespace.Name
			staticPodName = "static-pod-" + string(uuid.NewUUID())
			mirrorPodName = staticPodName + "-" + framework.TestContext.NodeName

			podPath = framework.TestContext.KubeletConfig.StaticPodPath
			ginkgo.By("create the static pod")
			err := createStaticPod(podPath, staticPodName, ns,
				imageutils.GetE2EImage(imageutils.Nginx), v1.RestartPolicyAlways)
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to be running")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRunning(ctx, f.ClientSet, mirrorPodName, ns)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())

			ginkgo.By("delete the pod manifest from disk")
			err = deleteStaticPod(podPath, staticPodName, ns)
			framework.ExpectNoError(err)

			ginkgo.By("recreate the file")
			err = createStaticPod(podPath, staticPodName, ns,
				imageutils.GetE2EImage(imageutils.Nginx), v1.RestartPolicyAlways)
			framework.ExpectNoError(err)

			ginkgo.By("mirror pod should restart with count 1")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodRunningWithRestartCount(ctx, 2*time.Second, 2*time.Minute, f.ClientSet, mirrorPodName, ns, 1)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())

			ginkgo.By("mirror pod should stay running")
			gomega.Consistently(ctx, func(ctx context.Context) error {
				return checkMirrorPodRunning(ctx, f.ClientSet, mirrorPodName, ns)
			}, time.Second*30, time.Second*4).Should(gomega.BeNil())

			ginkgo.By("delete the static pod")
			err = deleteStaticPod(podPath, staticPodName, ns)
			framework.ExpectNoError(err)

			ginkgo.By("wait for the mirror pod to disappear")
			gomega.Eventually(ctx, func(ctx context.Context) error {
				return checkMirrorPodDisappear(ctx, f.ClientSet, mirrorPodName, ns)
			}, 2*time.Minute, time.Second*4).Should(gomega.BeNil())
		})
	})
})

func staticPodPath(dir, name, namespace string) string {
	return filepath.Join(dir, namespace+"-"+name+".yaml")
}

func createStaticPod(dir, name, namespace, image string, restart v1.RestartPolicy) error {
	template := `
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  containers:
  - name: test
    image: %s
  restartPolicy: %s
`
	file := staticPodPath(dir, name, namespace)
	podYaml := fmt.Sprintf(template, name, namespace, image, string(restart))

	f, err := os.OpenFile(file, os.O_RDWR|os.O_TRUNC|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(podYaml)
	return err
}

func deleteStaticPod(dir, name, namespace string) error {
	file := staticPodPath(dir, name, namespace)
	return os.Remove(file)
}

func checkMirrorPodDisappear(ctx context.Context, cl clientset.Interface, name, namespace string) error {
	_, err := cl.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return goerrors.New("pod not disappear")
}

func checkMirrorPodRunning(ctx context.Context, cl clientset.Interface, name, namespace string) error {
	pod, err := cl.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("expected the mirror pod %q to appear: %w", name, err)
	}
	if pod.Status.Phase != v1.PodRunning {
		return fmt.Errorf("expected the mirror pod %q to be running, got %q", name, pod.Status.Phase)
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].State.Running == nil {
			return fmt.Errorf("expected the mirror pod %q with container %q to be running (got containers=%v)", name, pod.Status.ContainerStatuses[i].Name, pod.Status.ContainerStatuses[i].State)
		}
	}
	return validateMirrorPod(ctx, cl, pod)
}

func checkMirrorPodRunningWithRestartCount(ctx context.Context, interval time.Duration, timeout time.Duration, cl clientset.Interface, name, namespace string, count int32) error {
	var pod *v1.Pod
	var err error
	err = wait.PollImmediateWithContext(ctx, interval, timeout, func(ctx context.Context) (bool, error) {
		pod, err = cl.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("expected the mirror pod %q to appear: %w", name, err)
		}
		if pod.Status.Phase != v1.PodRunning {
			return false, fmt.Errorf("expected the mirror pod %q to be running, got %q", name, pod.Status.Phase)
		}
		for i := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[i].State.Waiting != nil {
				// retry if pod is in waiting state
				return false, nil
			}
			if pod.Status.ContainerStatuses[i].State.Running == nil {
				return false, fmt.Errorf("expected the mirror pod %q with container %q to be running (got containers=%v)", name, pod.Status.ContainerStatuses[i].Name, pod.Status.ContainerStatuses[i].State)
			}
			if pod.Status.ContainerStatuses[i].RestartCount == count {
				// found the restart count
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	return validateMirrorPod(ctx, cl, pod)
}

func checkMirrorPodRecreatedAndRunning(ctx context.Context, cl clientset.Interface, name, namespace string, oUID types.UID) error {
	pod, err := cl.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("expected the mirror pod %q to appear: %w", name, err)
	}
	if pod.UID == oUID {
		return fmt.Errorf("expected the uid of mirror pod %q to be changed, got %q", name, pod.UID)
	}
	if pod.Status.Phase != v1.PodRunning {
		return fmt.Errorf("expected the mirror pod %q to be running, got %q", name, pod.Status.Phase)
	}
	return validateMirrorPod(ctx, cl, pod)
}

func validateMirrorPod(ctx context.Context, cl clientset.Interface, mirrorPod *v1.Pod) error {
	hash, ok := mirrorPod.Annotations[kubetypes.ConfigHashAnnotationKey]
	if !ok || hash == "" {
		return fmt.Errorf("expected mirror pod %q to have a hash annotation", mirrorPod.Name)
	}
	mirrorHash, ok := mirrorPod.Annotations[kubetypes.ConfigMirrorAnnotationKey]
	if !ok || mirrorHash == "" {
		return fmt.Errorf("expected mirror pod %q to have a mirror pod annotation", mirrorPod.Name)
	}
	if hash != mirrorHash {
		return fmt.Errorf("expected mirror pod %q to have a matching mirror pod hash: got %q; expected %q", mirrorPod.Name, mirrorHash, hash)
	}
	source, ok := mirrorPod.Annotations[kubetypes.ConfigSourceAnnotationKey]
	if !ok {
		return fmt.Errorf("expected mirror pod %q to have a source annotation", mirrorPod.Name)
	}
	if source == kubetypes.ApiserverSource {
		return fmt.Errorf("expected mirror pod %q source to not be 'api'; got: %q", mirrorPod.Name, source)
	}

	if len(mirrorPod.OwnerReferences) != 1 {
		return fmt.Errorf("expected mirror pod %q to have a single owner reference: got %d", mirrorPod.Name, len(mirrorPod.OwnerReferences))
	}
	node, err := cl.CoreV1().Nodes().Get(ctx, framework.TestContext.NodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch test node: %w", err)
	}

	controller := true
	expectedOwnerRef := metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Node",
		Name:       framework.TestContext.NodeName,
		UID:        node.UID,
		Controller: &controller,
	}
	ref := mirrorPod.OwnerReferences[0]
	if !apiequality.Semantic.DeepEqual(ref, expectedOwnerRef) {
		return fmt.Errorf("unexpected mirror pod %q owner ref: %v", mirrorPod.Name, cmp.Diff(expectedOwnerRef, ref))
	}

	return nil
}
