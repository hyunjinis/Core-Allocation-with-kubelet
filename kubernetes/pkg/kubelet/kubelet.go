package kubelet

import (
        "context"
        "crypto/tls"
        "fmt"
        "math"
        "net"
        "net/http"
        "os"
        "path/filepath"
        sysruntime "runtime"
        "sort"
        "sync"
        "sync/atomic"
        "time"

        cadvisorapi "github.com/google/cadvisor/info/v1"
        "github.com/google/go-cmp/cmp"
        libcontaineruserns "github.com/opencontainers/runc/libcontainer/userns"
        "github.com/opencontainers/selinux/go-selinux"
        "go.opentelemetry.io/otel/attribute"
        "go.opentelemetry.io/otel/trace"
        "k8s.io/client-go/informers"
        utilfs "k8s.io/kubernetes/pkg/util/filesystem"
        "k8s.io/mount-utils"
        "k8s.io/utils/integer"
        netutils "k8s.io/utils/net"

        v1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/fields"
        "k8s.io/apimachinery/pkg/labels"
        "k8s.io/apimachinery/pkg/types"
        utilruntime "k8s.io/apimachinery/pkg/util/runtime"
        "k8s.io/apimachinery/pkg/util/sets"
        "k8s.io/apimachinery/pkg/util/wait"
        utilfeature "k8s.io/apiserver/pkg/util/feature"
        clientset "k8s.io/client-go/kubernetes"
        v1core "k8s.io/client-go/kubernetes/typed/core/v1"
        corelisters "k8s.io/client-go/listers/core/v1"
        "k8s.io/client-go/tools/cache"
        "k8s.io/client-go/tools/record"
        "k8s.io/client-go/util/certificate"
        "k8s.io/client-go/util/flowcontrol"
        cloudprovider "k8s.io/cloud-provider"
        "k8s.io/component-helpers/apimachinery/lease"
        internalapi "k8s.io/cri-api/pkg/apis"
        runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
        "k8s.io/klog/v2"
        pluginwatcherapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
        statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
        podutil "k8s.io/kubernetes/pkg/api/v1/pod"
        "k8s.io/kubernetes/pkg/api/v1/resource"
        "k8s.io/kubernetes/pkg/features"
        kubeletconfiginternal "k8s.io/kubernetes/pkg/kubelet/apis/config"
        "k8s.io/kubernetes/pkg/kubelet/apis/podresources"
        "k8s.io/kubernetes/pkg/kubelet/cadvisor"
        kubeletcertificate "k8s.io/kubernetes/pkg/kubelet/certificate"
        "k8s.io/kubernetes/pkg/kubelet/cloudresource"
        "k8s.io/kubernetes/pkg/kubelet/cm"
        draplugin "k8s.io/kubernetes/pkg/kubelet/cm/dra/plugin"
        "k8s.io/kubernetes/pkg/kubelet/config"
        "k8s.io/kubernetes/pkg/kubelet/configmap"
        kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
        "k8s.io/kubernetes/pkg/kubelet/cri/remote"
        "k8s.io/kubernetes/pkg/kubelet/events"
        "k8s.io/kubernetes/pkg/kubelet/eviction"
        "k8s.io/kubernetes/pkg/kubelet/images"
        "k8s.io/kubernetes/pkg/kubelet/kuberuntime"
        "k8s.io/kubernetes/pkg/kubelet/lifecycle"
        "k8s.io/kubernetes/pkg/kubelet/logs"
        "k8s.io/kubernetes/pkg/kubelet/metrics"
        "k8s.io/kubernetes/pkg/kubelet/metrics/collectors"
        "k8s.io/kubernetes/pkg/kubelet/network/dns"
        "k8s.io/kubernetes/pkg/kubelet/nodeshutdown"
        oomwatcher "k8s.io/kubernetes/pkg/kubelet/oom"
        "k8s.io/kubernetes/pkg/kubelet/pleg"
        "k8s.io/kubernetes/pkg/kubelet/pluginmanager"
        plugincache "k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"
        kubepod "k8s.io/kubernetes/pkg/kubelet/pod"
        "k8s.io/kubernetes/pkg/kubelet/preemption"
        "k8s.io/kubernetes/pkg/kubelet/prober"
        proberesults "k8s.io/kubernetes/pkg/kubelet/prober/results"
        "k8s.io/kubernetes/pkg/kubelet/runtimeclass"
        "k8s.io/kubernetes/pkg/kubelet/secret"
        "k8s.io/kubernetes/pkg/kubelet/server"
        servermetrics "k8s.io/kubernetes/pkg/kubelet/server/metrics"
        serverstats "k8s.io/kubernetes/pkg/kubelet/server/stats"
        "k8s.io/kubernetes/pkg/kubelet/stats"
        "k8s.io/kubernetes/pkg/kubelet/status"
        "k8s.io/kubernetes/pkg/kubelet/sysctl"
        "k8s.io/kubernetes/pkg/kubelet/token"
        kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
        "k8s.io/kubernetes/pkg/kubelet/userns"
        "k8s.io/kubernetes/pkg/kubelet/util"
        "k8s.io/kubernetes/pkg/kubelet/util/manager"
        "k8s.io/kubernetes/pkg/kubelet/util/queue"
        "k8s.io/kubernetes/pkg/kubelet/util/sliceutils"
        "k8s.io/kubernetes/pkg/kubelet/volumemanager"
        httpprobe "k8s.io/kubernetes/pkg/probe/http"
        "k8s.io/kubernetes/pkg/security/apparmor"
        "k8s.io/kubernetes/pkg/util/oom"
        "k8s.io/kubernetes/pkg/volume"
        "k8s.io/kubernetes/pkg/volume/csi"
        "k8s.io/kubernetes/pkg/volume/util/hostutil"
        "k8s.io/kubernetes/pkg/volume/util/subpath"
        "k8s.io/kubernetes/pkg/volume/util/volumepathhandler"
        "k8s.io/utils/clock"
        "k8s.io/kubernetes/pkg/kubelet/necon"
)

func (kl *Kubelet) SyncPod(ctx context.Context, updateType kubetypes.SyncPodType, pod, mirrorPod *v1.Pod, podStatus *kubecontainer.PodStatus) (isTerminal bool, err error) {
        ctx, otelSpan := kl.tracer.Start(ctx, "syncPod", trace.WithAttributes(
                attribute.String("k8s.pod.uid", string(pod.UID)),
                attribute.String("k8s.pod", klog.KObj(pod).String()),
                attribute.String("k8s.pod.name", pod.Name),
                attribute.String("k8s.pod.update_type", updateType.String()),
                attribute.String("k8s.namespace.name", pod.Namespace),
        ))
        klog.V(4).InfoS("SyncPod enter", "pod", klog.KObj(pod), "podUID", pod.UID)
        defer func() {
                klog.V(4).InfoS("SyncPod exit", "pod", klog.KObj(pod), "podUID", pod.UID, "isTerminal", isTerminal)
                otelSpan.End()
        }()

        // Latency measurements for the main workflow are relative to the
        // first time the pod was seen by kubelet.
        var firstSeenTime time.Time
        if firstSeenTimeStr, ok := pod.Annotations[kubetypes.ConfigFirstSeenAnnotationKey]; ok {
                firstSeenTime = kubetypes.ConvertToTimestamp(firstSeenTimeStr).Get()
        }

        // Record pod worker start latency if being created
        // TODO: make pod workers record their own latencies
        if updateType == kubetypes.SyncPodCreate {
                if !firstSeenTime.IsZero() {
                        // This is the first time we are syncing the pod. Record the latency
                        // since kubelet first saw the pod if firstSeenTime is set.
                        metrics.PodWorkerStartDuration.Observe(metrics.SinceInSeconds(firstSeenTime))
                } else {
                        klog.V(3).InfoS("First seen time not recorded for pod",
                                "podUID", pod.UID,
                                "pod", klog.KObj(pod))
                }
        }
        // Generate final API pod status with pod and status manager status
        apiPodStatus := kl.generateAPIPodStatus(pod, podStatus, false)
        // The pod IP may be changed in generateAPIPodStatus if the pod is using host network. (See #24576)
        // TODO(random-liu): After writing pod spec into container labels, check whether pod is using host network, and
        // set pod IP to hostIP directly in runtime.GetPodStatus
        podStatus.IPs = make([]string, 0, len(apiPodStatus.PodIPs))
        for _, ipInfo := range apiPodStatus.PodIPs {
                podStatus.IPs = append(podStatus.IPs, ipInfo.IP)
        }
        if len(podStatus.IPs) == 0 && len(apiPodStatus.PodIP) > 0 {
                podStatus.IPs = []string{apiPodStatus.PodIP}
        }

        // If the pod is terminal, we don't need to continue to setup the pod
        if apiPodStatus.Phase == v1.PodSucceeded || apiPodStatus.Phase == v1.PodFailed {
                kl.statusManager.SetPodStatus(pod, apiPodStatus)
                isTerminal = true
                return isTerminal, nil
        }

        // If the pod should not be running, we request the pod's containers be stopped. This is not the same
        // as termination (we want to stop the pod, but potentially restart it later if soft admission allows
        // it later). Set the status and phase appropriately
        runnable := kl.canRunPod(pod)
        if !runnable.Admit {
                // Pod is not runnable; and update the Pod and Container statuses to why.
                if apiPodStatus.Phase != v1.PodFailed && apiPodStatus.Phase != v1.PodSucceeded {
                        apiPodStatus.Phase = v1.PodPending
                }
                apiPodStatus.Reason = runnable.Reason
                apiPodStatus.Message = runnable.Message
                // Waiting containers are not creating.
                const waitingReason = "Blocked"
                for _, cs := range apiPodStatus.InitContainerStatuses {
                        if cs.State.Waiting != nil {
                                cs.State.Waiting.Reason = waitingReason
                        }
                }
                for _, cs := range apiPodStatus.ContainerStatuses {
                        if cs.State.Waiting != nil {
                                cs.State.Waiting.Reason = waitingReason
                        }
                }
        }

        // Record the time it takes for the pod to become running
        // since kubelet first saw the pod if firstSeenTime is set.
        existingStatus, ok := kl.statusManager.GetPodStatus(pod.UID)
        if !ok || existingStatus.Phase == v1.PodPending && apiPodStatus.Phase == v1.PodRunning &&
                !firstSeenTime.IsZero() {
                metrics.PodStartDuration.Observe(metrics.SinceInSeconds(firstSeenTime))
        }

        kl.statusManager.SetPodStatus(pod, apiPodStatus)

        // Pods that are not runnable must be stopped - return a typed error to the pod worker
        if !runnable.Admit {
                klog.V(2).InfoS("Pod is not runnable and must have running containers stopped", "pod", klog.KObj(pod), "podUID", pod.UID, "message", runnable.Message)
                var syncErr error
                p := kubecontainer.ConvertPodStatusToRunningPod(kl.getRuntime().Type(), podStatus)
                if err := kl.killPod(ctx, pod, p, nil); err != nil {
                        if !wait.Interrupted(err) {
                                kl.recorder.Eventf(pod, v1.EventTypeWarning, events.FailedToKillPod, "error killing pod: %v", err)
                                syncErr = fmt.Errorf("error killing pod: %w", err)
                                utilruntime.HandleError(syncErr)
                        }
                } else {
                        // There was no error killing the pod, but the pod cannot be run.
                        // Return an error to signal that the sync loop should back off.
                        syncErr = fmt.Errorf("pod cannot be run: %v", runnable.Message)
                }
                return false, syncErr
        }

        // If the network plugin is not ready, only start the pod if it uses the host network
        if err := kl.runtimeState.networkErrors(); err != nil && !kubecontainer.IsHostNetworkPod(pod) {
                kl.recorder.Eventf(pod, v1.EventTypeWarning, events.NetworkNotReady, "%s: %v", NetworkNotReadyErrorMsg, err)
                return false, fmt.Errorf("%s: %v", NetworkNotReadyErrorMsg, err)
        }

        // ensure the kubelet knows about referenced secrets or configmaps used by the pod
        if !kl.podWorkers.IsPodTerminationRequested(pod.UID) {
                if kl.secretManager != nil {
                        kl.secretManager.RegisterPod(pod)
                }
                if kl.configMapManager != nil {
                        kl.configMapManager.RegisterPod(pod)
                }
        }

        // Create Cgroups for the pod and apply resource parameters
        // to them if cgroups-per-qos flag is enabled.
        pcm := kl.containerManager.NewPodContainerManager()
        // If pod has already been terminated then we need not create
        // or update the pod's cgroup
        // TODO: once context cancellation is added this check can be removed
        if !kl.podWorkers.IsPodTerminationRequested(pod.UID) {
                // When the kubelet is restarted with the cgroups-per-qos
                // flag enabled, all the pod's running containers
                // should be killed intermittently and brought back up
                // under the qos cgroup hierarchy.
                // Check if this is the pod's first sync
                firstSync := true
                for _, containerStatus := range apiPodStatus.ContainerStatuses {
                        if containerStatus.State.Running != nil {
                                firstSync = false
                                break
                        }
                }
                // Don't kill containers in pod if pod's cgroups already
                // exists or the pod is running for the first time
                podKilled := false
                if !pcm.Exists(pod) && !firstSync {
                        p := kubecontainer.ConvertPodStatusToRunningPod(kl.getRuntime().Type(), podStatus)
                        if err := kl.killPod(ctx, pod, p, nil); err == nil {
                                if wait.Interrupted(err) {
                                        return false, err
                                }
                                podKilled = true
                        } else {
                                klog.ErrorS(err, "KillPod failed", "pod", klog.KObj(pod), "podStatus", podStatus)
                        }
                }
                // Create and Update pod's Cgroups
                // Don't create cgroups for run once pod if it was killed above
                // The current policy is not to restart the run once pods when
                // the kubelet is restarted with the new flag as run once pods are
                // expected to run only once and if the kubelet is restarted then
                // they are not expected to run again.
                // We don't create and apply updates to cgroup if its a run once pod and was killed above
                if !(podKilled && pod.Spec.RestartPolicy == v1.RestartPolicyNever) {
                        if !pcm.Exists(pod) {
                                //************modified
                                n := necon.GetInstance()
                                fmt.Println("pod : ",*pod)
                                n.SetNeconPod(*pod)
                                //********************
                                if err := kl.containerManager.UpdateQOSCgroups(); err != nil {
                                        klog.V(2).InfoS("Failed to update QoS cgroups while syncing pod", "pod", klog.KObj(pod), "err", err)
                                }
                                if err := pcm.EnsureExists(pod); err != nil {
                                        kl.recorder.Eventf(pod, v1.EventTypeWarning, events.FailedToCreatePodContainer, "unable to ensure pod container exists: %v", err)
                                }
                        }
                }
        }

        // Create Mirror Pod for Static Pod if it doesn't already exist
        if kubetypes.IsStaticPod(pod) {
                deleted := false
                if mirrorPod != nil {
                        if mirrorPod.DeletionTimestamp != nil || !kubepod.IsMirrorPodOf(mirrorPod, pod) {
                                // The mirror pod is semantically different from the static pod. Remove
                                // it. The mirror pod will get recreated later.
                                klog.InfoS("Trying to delete pod", "pod", klog.KObj(pod), "podUID", mirrorPod.ObjectMeta.UID)
                                podFullName := kubecontainer.GetPodFullName(pod)
                                var err error
                                deleted, err = kl.mirrorPodClient.DeleteMirrorPod(podFullName, &mirrorPod.ObjectMeta.UID)
                                if deleted {
                                        klog.InfoS("Deleted mirror pod because it is outdated", "pod", klog.KObj(mirrorPod))
                                } else if err != nil {
                                        klog.ErrorS(err, "Failed deleting mirror pod", "pod", klog.KObj(mirrorPod))
                                }
                        }
                }
                if mirrorPod == nil || deleted {
                        node, err := kl.GetNode()
                        if err != nil || node.DeletionTimestamp != nil {
                                klog.V(4).InfoS("No need to create a mirror pod, since node has been removed from the cluster", "node", klog.KRef("", string(kl.nodeName)))
                        } else {
                                klog.V(4).InfoS("Creating a mirror pod for static pod", "pod", klog.KObj(pod))
                                if err := kl.mirrorPodClient.CreateMirrorPod(pod); err != nil {
                                        klog.ErrorS(err, "Failed creating a mirror pod for", "pod", klog.KObj(pod))
                                }
                        }
                }
        }
        // Make data directories for the pod
        if err := kl.makePodDataDirs(pod); err != nil {
                kl.recorder.Eventf(pod, v1.EventTypeWarning, events.FailedToMakePodDataDirectories, "error making pod data directories: %v", err)
                klog.ErrorS(err, "Unable to make pod data directories for pod", "pod", klog.KObj(pod))
                return false, err
        }

        // Wait for volumes to attach/mount
        if err := kl.volumeManager.WaitForAttachAndMount(ctx, pod); err != nil {
                if !wait.Interrupted(err) {
                        kl.recorder.Eventf(pod, v1.EventTypeWarning, events.FailedMountVolume, "Unable to attach or mount volumes: %v", err)
                        klog.ErrorS(err, "Unable to attach or mount volumes for pod; skipping pod", "pod", klog.KObj(pod))
                }
                return false, err
        }

        // Fetch the pull secrets for the pod
        pullSecrets := kl.getPullSecretsForPod(pod)

        // Ensure the pod is being probed
        kl.probeManager.AddPod(pod)

        if utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodVerticalScaling) {
                // Handle pod resize here instead of doing it in HandlePodUpdates because
                // this conveniently retries any Deferred resize requests
                // TODO(vinaykul,InPlacePodVerticalScaling): Investigate doing this in HandlePodUpdates + periodic SyncLoop scan
                //     See: https://github.com/kubernetes/kubernetes/pull/102884#discussion_r663160060
                if kl.podWorkers.CouldHaveRunningContainers(pod.UID) && !kubetypes.IsStaticPod(pod) {
                        pod = kl.handlePodResourcesResize(pod)
                }
        }

        // TODO(#113606): connect this with the incoming context parameter, which comes from the pod worker.
        // Currently, using that context causes test failures. To remove this todoCtx, any wait.Interrupted
        // errors need to be filtered from result and bypass the reasonCache - cancelling the context for
        // SyncPod is a known and deliberate error, not a generic error.
        todoCtx := context.TODO()
        // Call the container runtime's SyncPod callback
        result := kl.containerRuntime.SyncPod(todoCtx, pod, podStatus, pullSecrets, kl.backOff)
        kl.reasonCache.Update(pod.UID, result)
        if err := result.Error(); err != nil {
                // Do not return error if the only failures were pods in backoff
                for _, r := range result.SyncResults {
                        if r.Error != kubecontainer.ErrCrashLoopBackOff && r.Error != images.ErrImagePullBackOff {
                                // Do not record an event here, as we keep all event logging for sync pod failures
                                // local to container runtime, so we get better errors.
                                return false, err
                        }
                }

                return false, nil
        }

        if utilfeature.DefaultFeatureGate.Enabled(features.InPlacePodVerticalScaling) && isPodResizeInProgress(pod, &apiPodStatus) {
                // While resize is in progress, periodically call PLEG to update pod cache
                runningPod := kubecontainer.ConvertPodStatusToRunningPod(kl.getRuntime().Type(), podStatus)
                if err, _ := kl.pleg.UpdateCache(&runningPod, pod.UID); err != nil {
                        klog.ErrorS(err, "Failed to update pod cache", "pod", klog.KObj(pod))
                        return false, err
                }
        }

        return false, nil
}

