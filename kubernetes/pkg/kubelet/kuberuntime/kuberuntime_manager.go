func (m *kubeGenericRuntimeManager) SyncPod(ctx context.Context, pod *v1.Pod, podStatus *kubecontainer.PodStatus, pullSecrets []v1.Secret, backOff *flowcontrol.Backoff) (result kubecontainer.PodSyncResult) {
        // Step 1: Compute sandbox and container changes.
        podContainerChanges := m.computePodActions(ctx, pod, podStatus)
        klog.V(3).InfoS("computePodActions got for pod", "podActions", podContainerChanges, "pod", klog.KObj(pod))
        if podContainerChanges.CreateSandbox {
                ref, err := ref.GetReference(legacyscheme.Scheme, pod)
                if err != nil {
                        klog.ErrorS(err, "Couldn't make a ref to pod", "pod", klog.KObj(pod))
                }
                if podContainerChanges.SandboxID != "" {
                        m.recorder.Eventf(ref, v1.EventTypeNormal, events.SandboxChanged, "Pod sandbox changed, it will be killed and re-created.")
                } else {
                        klog.V(4).InfoS("SyncPod received new pod, will create a sandbox for it", "pod", klog.KObj(pod))
                }
        }

        // Step 2: Kill the pod if the sandbox has changed.
        if podContainerChanges.KillPod {
                if podContainerChanges.CreateSandbox {
                        klog.V(4).InfoS("Stopping PodSandbox for pod, will start new one", "pod", klog.KObj(pod))
                } else {
                        klog.V(4).InfoS("Stopping PodSandbox for pod, because all other containers are dead", "pod", klog.KObj(pod))
                }

                killResult := m.killPodWithSyncResult(ctx, pod, kubecontainer.ConvertPodStatusToRunningPod(m.runtimeName, podStatus), nil)
                result.AddPodSyncResult(killResult)
                if killResult.Error() != nil {
                        klog.ErrorS(killResult.Error(), "killPodWithSyncResult failed")
                        return
                }

                if podContainerChanges.CreateSandbox {
                        m.purgeInitContainers(ctx, pod, podStatus)
                }
        } else {
                // Step 3: kill any running containers in this pod which are not to keep.
                for containerID, containerInfo := range podContainerChanges.ContainersToKill {
                        klog.V(3).InfoS("Killing unwanted container for pod", "containerName", containerInfo.name, "containerID", containerID, "pod", klog.KObj(pod))
                        killContainerResult := kubecontainer.NewSyncResult(kubecontainer.KillContainer, containerInfo.name)
                        result.AddSyncResult(killContainerResult)
                        if err := m.killContainer(ctx, pod, containerID, containerInfo.name, containerInfo.message, containerInfo.reason, nil); err != nil {
                                killContainerResult.Fail(kubecontainer.ErrKillContainer, err.Error())
                                klog.ErrorS(err, "killContainer for pod failed", "containerName", containerInfo.name, "containerID", containerID, "pod", klog.KObj(pod))
                                return
                        }
                }
        }

        // Keep terminated init containers fairly aggressively controlled
        // This is an optimization because container removals are typically handled
        // by container garbage collector.
        m.pruneInitContainersBeforeStart(ctx, pod, podStatus)

        // We pass the value of the PRIMARY podIP and list of podIPs down to
        // generatePodSandboxConfig and generateContainerConfig, which in turn
        // passes it to various other functions, in order to facilitate functionality
        // that requires this value (hosts file and downward API) and avoid races determining
        // the pod IP in cases where a container requires restart but the
        // podIP isn't in the status manager yet. The list of podIPs is used to
        // generate the hosts file.
        //
        // We default to the IPs in the passed-in pod status, and overwrite them if the
        // sandbox needs to be (re)started.
        var podIPs []string
        if podStatus != nil {
                podIPs = podStatus.IPs
        }
        // Step 4: Create a sandbox for the pod if necessary.
        podSandboxID := podContainerChanges.SandboxID
        if podContainerChanges.CreateSandbox {
                var msg string
                var err error

                klog.V(4).InfoS("Creating PodSandbox for pod", "pod", klog.KObj(pod))
                metrics.StartedPodsTotal.Inc()
                createSandboxResult := kubecontainer.NewSyncResult(kubecontainer.CreatePodSandbox, format.Pod(pod))
                result.AddSyncResult(createSandboxResult)

                // ConvertPodSysctlsVariableToDotsSeparator converts sysctl variable
                // in the Pod.Spec.SecurityContext.Sysctls slice into a dot as a separator.
                // runc uses the dot as the separator to verify whether the sysctl variable
                // is correct in a separate namespace, so when using the slash as the sysctl
                // variable separator, runc returns an error: "sysctl is not in a separate kernel namespace"
                // and the podSandBox cannot be successfully created. Therefore, before calling runc,
                // we need to convert the sysctl variable, the dot is used as a separator to separate the kernel namespace.
                // When runc supports slash as sysctl separator, this function can no longer be used.
                sysctl.ConvertPodSysctlsVariableToDotsSeparator(pod.Spec.SecurityContext)

                // Prepare resources allocated by the Dynammic Resource Allocation feature for the pod
                if utilfeature.DefaultFeatureGate.Enabled(features.DynamicResourceAllocation) {
                        if err := m.runtimeHelper.PrepareDynamicResources(pod); err != nil {
                                ref, referr := ref.GetReference(legacyscheme.Scheme, pod)
                                if referr != nil {
                                        klog.ErrorS(referr, "Couldn't make a ref to pod", "pod", klog.KObj(pod))
                                        return
                                }
                                m.recorder.Eventf(ref, v1.EventTypeWarning, events.FailedPrepareDynamicResources, "Failed to prepare dynamic resources: %v", err)
                                klog.ErrorS(err, "Failed to prepare dynamic resources", "pod", klog.KObj(pod))
                                return
                        }
                }

                podSandboxID, msg, err = m.createPodSandbox(ctx, pod, podContainerChanges.Attempt)
                if err != nil {
                        // createPodSandbox can return an error from CNI, CSI,
                        // or CRI if the Pod has been deleted while the POD is
                        // being created. If the pod has been deleted then it's
                        // not a real error.
                        //
                        // SyncPod can still be running when we get here, which
                        // means the PodWorker has not acked the deletion.
                        if m.podStateProvider.IsPodTerminationRequested(pod.UID) {
                                klog.V(4).InfoS("Pod was deleted and sandbox failed to be created", "pod", klog.KObj(pod), "podUID", pod.UID)
                                return
                        }
                        metrics.StartedPodsErrorsTotal.Inc()
                        createSandboxResult.Fail(kubecontainer.ErrCreatePodSandbox, msg)
                        klog.ErrorS(err, "CreatePodSandbox for pod failed", "pod", klog.KObj(pod))
                        ref, referr := ref.GetReference(legacyscheme.Scheme, pod)
                        if referr != nil {
                                klog.ErrorS(referr, "Couldn't make a ref to pod", "pod", klog.KObj(pod))
                        }
                        m.recorder.Eventf(ref, v1.EventTypeWarning, events.FailedCreatePodSandBox, "Failed to create pod sandbox: %v", err)
                        return
                }
                klog.V(4).InfoS("Created PodSandbox for pod", "podSandboxID", podSandboxID, "pod", klog.KObj(pod))

                resp, err := m.runtimeService.PodSandboxStatus(ctx, podSandboxID, false)
                if err != nil {
                        ref, referr := ref.GetReference(legacyscheme.Scheme, pod)
                        if referr != nil {
                                klog.ErrorS(referr, "Couldn't make a ref to pod", "pod", klog.KObj(pod))
                        }
                        m.recorder.Eventf(ref, v1.EventTypeWarning, events.FailedStatusPodSandBox, "Unable to get pod sandbox status: %v", err)
                        klog.ErrorS(err, "Failed to get pod sandbox status; Skipping pod", "pod", klog.KObj(pod))
                        result.Fail(err)
                        return
                }
                if resp.GetStatus() == nil {
                        result.Fail(errors.New("pod sandbox status is nil"))
                        return
                }

                // If we ever allow updating a pod from non-host-network to
                // host-network, we may use a stale IP.
                if !kubecontainer.IsHostNetworkPod(pod) {
                        // Overwrite the podIPs passed in the pod status, since we just started the pod sandbox.
                        podIPs = m.determinePodSandboxIPs(pod.Namespace, pod.Name, resp.GetStatus())
                        klog.V(4).InfoS("Determined the ip for pod after sandbox changed", "IPs", podIPs, "pod", klog.KObj(pod))
                }
                //******modified
                n := necon.GetInstance()
                err = n.ApplyCPUAffinity(*pod)
                if err != nil {
                        klog.Errorf("CPU affinity 설정 실패: %v", err)
                } else {
                        klog.Infof("Successfully set CPU affinity for Pod UID: %s", pod.ObjectMeta.UID)
                }
                //*************

        }

        // the start containers routines depend on pod ip(as in primary pod ip)
        // instead of trying to figure out if we have 0 < len(podIPs)
        // everytime, we short circuit it here
        podIP := ""
        if len(podIPs) != 0 {
                podIP = podIPs[0]
        }

        // Get podSandboxConfig for containers to start.
        configPodSandboxResult := kubecontainer.NewSyncResult(kubecontainer.ConfigPodSandbox, podSandboxID)
        result.AddSyncResult(configPodSandboxResult)
        podSandboxConfig, err := m.generatePodSandboxConfig(pod, podContainerChanges.Attempt)
        if err != nil {
                message := fmt.Sprintf("GeneratePodSandboxConfig for pod %q failed: %v", format.Pod(pod), err)
                klog.ErrorS(err, "GeneratePodSandboxConfig for pod failed", "pod", klog.KObj(pod))
                configPodSandboxResult.Fail(kubecontainer.ErrConfigPodSandbox, message)
                return
        }

        // Helper containing boilerplate common to starting all types of containers.
        // typeName is a description used to describe this type of container in log messages,
        // currently: "container", "init container" or "ephemeral container"
        // metricLabel is the label used to describe this type of container in monitoring metrics.
        // currently: "container", "init_container" or "ephemeral_container"
        start := func(ctx context.Context, typeName, metricLabel string, spec *startSpec) error {
                startContainerResult := kubecontainer.NewSyncResult(kubecontainer.StartContainer, spec.container.Name)
                result.AddSyncResult(startContainerResult)

                isInBackOff, msg, err := m.doBackOff(pod, spec.container, podStatus, backOff)
                if isInBackOff {
                        startContainerResult.Fail(err, msg)
                        klog.V(4).InfoS("Backing Off restarting container in pod", "containerType", typeName, "container", spec.container, "pod", klog.KObj(pod))
                        return err
                }

                metrics.StartedContainersTotal.WithLabelValues(metricLabel).Inc()
                if sc.HasWindowsHostProcessRequest(pod, spec.container) {
                        metrics.StartedHostProcessContainersTotal.WithLabelValues(metricLabel).Inc()
                }
                klog.V(4).InfoS("Creating container in pod", "containerType", typeName, "container", spec.container, "pod", klog.KObj(pod))
                // NOTE (aramase) podIPs are populated for single stack and dual stack clusters. Send only podIPs.
                if msg, err := m.startContainer(ctx, podSandboxID, podSandboxConfig, spec, pod, podStatus, pullSecrets, podIP, podIPs); err != nil {
                        // startContainer() returns well-defined error codes that have reasonable cardinality for metrics and are
                        // useful to cluster administrators to distinguish "server errors" from "user errors".
                        metrics.StartedContainersErrorsTotal.WithLabelValues(metricLabel, err.Error()).Inc()
                        if sc.HasWindowsHostProcessRequest(pod, spec.container) {
                                metrics.StartedHostProcessContainersErrorsTotal.WithLabelValues(metricLabel, err.Error()).Inc()
                        }
                        startContainerResult.Fail(err, msg)
                        // known errors that are logged in other places are logged at higher levels here to avoid
                        // repetitive log spam
                        switch {
                        case err == images.ErrImagePullBackOff:
                                klog.V(3).InfoS("Container start failed in pod", "containerType", typeName, "container", spec.container, "pod", klog.KObj(pod), "containerMessage", msg, "err", err)
                        default:
                                utilruntime.HandleError(fmt.Errorf("%v %+v start failed in pod %v: %v: %s", typeName, spec.container, format.Pod(pod), err, msg))
                        }
                        return err
                }

                return nil
        }

        // Step 5: start ephemeral containers
        // These are started "prior" to init containers to allow running ephemeral containers even when there
        // are errors starting an init container. In practice init containers will start first since ephemeral
        // containers cannot be specified on pod creation.
        for _, idx := range podContainerChanges.EphemeralContainersToStart {
                start(ctx, "ephemeral container", metrics.EphemeralContainer, ephemeralContainerStartSpec(&pod.Spec.EphemeralContainers[idx]))
        }

        if !utilfeature.DefaultFeatureGate.Enabled(features.SidecarContainers) {
                // Step 6: start the init container.
                if container := podContainerChanges.NextInitContainerToStart; container != nil {
                        // Start the next init container.
                        if err := start(ctx, "init container", metrics.InitContainer, containerStartSpec(container)); err != nil {
                                return
                        }

                        // Successfully started the container; clear the entry in the failure
                        klog.V(4).InfoS("Completed init container for pod", "containerName", container.Name, "pod", klog.KObj(pod))
                }
        } else {
                // Step 6: start init containers.
                for _, idx := range podContainerChanges.InitContainersToStart {
                        container := &pod.Spec.InitContainers[idx]
                        // Start the next init container.
                        if err := start(ctx, "init container", metrics.InitContainer, containerStartSpec(container)); err != nil {
                                if types.IsRestartableInitContainer(container) {
                                        klog.V(4).InfoS("Failed to start the restartable init container for the pod, skipping", "initContainerName", container.Name, "pod", klog.KObj(pod))
                                        continue
                                }
                                klog.V(4).InfoS("Failed to initialize the pod, as the init container failed to start, aborting", "initContainerName", container.Name, "pod", klog.KObj(pod))
                                return
                        }

                        // Successfully started the container; clear the entry in the failure
                        klog.V(4).InfoS("Completed init container for pod", "containerName", container.Name, "pod", klog.KObj(pod))
                }
        }

        // Step 7: For containers in podContainerChanges.ContainersToUpdate[CPU,Memory] list, invoke UpdateContainerResources
        if isInPlacePodVerticalScalingAllowed(pod) {
                if len(podContainerChanges.ContainersToUpdate) > 0 || podContainerChanges.UpdatePodResources {
                        m.doPodResizeAction(pod, podStatus, podContainerChanges, result)
                }
        }

        // Step 8: start containers in podContainerChanges.ContainersToStart.
        for _, idx := range podContainerChanges.ContainersToStart {
                start(ctx, "container", metrics.Container, containerStartSpec(&pod.Spec.Containers[idx]))
        }

        return
}
