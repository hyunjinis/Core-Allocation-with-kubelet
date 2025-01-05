func (m *kubeGenericRuntimeManager) startContainer(ctx context.Context, podSandboxID string, podSandboxConfig *runtimeapi.PodSandboxConfig, spec *startSpec, pod *v1.Pod, podStatus *kubecontainer.PodStatus, pullSecrets []v1.Secret, podIP string, podIPs []string) (string, error) {
        container := spec.container

        // Step 1: pull the image.
        imageRef, msg, err := m.imagePuller.EnsureImageExists(ctx, pod, container, pullSecrets, podSandboxConfig)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, "", v1.EventTypeWarning, events.FailedToCreateContainer, "Error: %v", s.Message())
                return msg, err
        }

        // Step 2: create the container.
        // For a new container, the RestartCount should be 0
        restartCount := 0
        containerStatus := podStatus.FindContainerStatusByName(container.Name)
        if containerStatus != nil {
                restartCount = containerStatus.RestartCount + 1
        } else {
                // The container runtime keeps state on container statuses and
                // what the container restart count is. When nodes are rebooted
                // some container runtimes clear their state which causes the
                // restartCount to be reset to 0. This causes the logfile to
                // start at 0.log, which either overwrites or appends to the
                // already existing log.
                //
                // We are checking to see if the log directory exists, and find
                // the latest restartCount by checking the log name -
                // {restartCount}.log - and adding 1 to it.
                logDir := BuildContainerLogsDirectory(pod.Namespace, pod.Name, pod.UID, container.Name)
                restartCount, err = calcRestartCountByLogDir(logDir)
                if err != nil {
                        klog.InfoS("Cannot calculate restartCount from the log directory", "logDir", logDir, "err", err)
                        restartCount = 0
                }
        }
        target, err := spec.getTargetID(podStatus)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, "", v1.EventTypeWarning, events.FailedToCreateContainer, "Error: %v", s.Message())
                return s.Message(), ErrCreateContainerConfig
        }

        containerConfig, cleanupAction, err := m.generateContainerConfig(ctx, container, pod, restartCount, podIP, imageRef, podIPs, target)
        if cleanupAction != nil {
                defer cleanupAction()
        }
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, "", v1.EventTypeWarning, events.FailedToCreateContainer, "Error: %v", s.Message())
                return s.Message(), ErrCreateContainerConfig
        }

        err = m.internalLifecycle.PreCreateContainer(pod, container, containerConfig)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, "", v1.EventTypeWarning, events.FailedToCreateContainer, "Internal PreCreateContainer hook failed: %v", s.Message())
                return s.Message(), ErrPreCreateHook
        }

        containerID, err := m.runtimeService.CreateContainer(ctx, podSandboxID, containerConfig, podSandboxConfig)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, containerID, v1.EventTypeWarning, events.FailedToCreateContainer, "Error: %v", s.Message())
                return s.Message(), ErrCreateContainer
        }
        err = m.internalLifecycle.PreStartContainer(pod, container, containerID)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, containerID, v1.EventTypeWarning, events.FailedToStartContainer, "Internal PreStartContainer hook failed: %v", s.Message())
                return s.Message(), ErrPreStartHook
        }
        m.recordContainerEvent(pod, container, containerID, v1.EventTypeNormal, events.CreatedContainer, fmt.Sprintf("Created container %s", container.Name))

        // Step 3: start the container.
        err = m.runtimeService.StartContainer(ctx, containerID)
        if err != nil {
                s, _ := grpcstatus.FromError(err)
                m.recordContainerEvent(pod, container, containerID, v1.EventTypeWarning, events.FailedToStartContainer, "Error: %v", s.Message())
                return s.Message(), kubecontainer.ErrRunContainer
        }
        m.recordContainerEvent(pod, container, containerID, v1.EventTypeNormal, events.StartedContainer, fmt.Sprintf("Started container %s", container.Name))

        // Symlink container logs to the legacy container log location for cluster logging
        // support.
        // TODO(random-liu): Remove this after cluster logging supports CRI container log path.
        containerMeta := containerConfig.GetMetadata()
        sandboxMeta := podSandboxConfig.GetMetadata()
        legacySymlink := legacyLogSymlink(containerID, containerMeta.Name, sandboxMeta.Name,
                sandboxMeta.Namespace)
        containerLog := filepath.Join(podSandboxConfig.LogDirectory, containerConfig.LogPath)
        // only create legacy symlink if containerLog path exists (or the error is not IsNotExist).
        // Because if containerLog path does not exist, only dangling legacySymlink is created.
        // This dangling legacySymlink is later removed by container gc, so it does not make sense
        // to create it in the first place. it happens when journald logging driver is used with docker.
        if _, err := m.osInterface.Stat(containerLog); !os.IsNotExist(err) {
                if err := m.osInterface.Symlink(containerLog, legacySymlink); err != nil {
                        klog.ErrorS(err, "Failed to create legacy symbolic link", "path", legacySymlink,
                                "containerID", containerID, "containerLogPath", containerLog)
                }
        }

        // Step 4: execute the post start hook.
        if container.Lifecycle != nil && container.Lifecycle.PostStart != nil {
                kubeContainerID := kubecontainer.ContainerID{
                        Type: m.runtimeName,
                        ID:   containerID,
                }
                msg, handlerErr := m.runner.Run(ctx, kubeContainerID, pod, container, container.Lifecycle.PostStart)
                if handlerErr != nil {
                        klog.ErrorS(handlerErr, "Failed to execute PostStartHook", "pod", klog.KObj(pod),
                                "podUID", pod.UID, "containerName", container.Name, "containerID", kubeContainerID.String())
                        // do not record the message in the event so that secrets won't leak from the server.
                        m.recordContainerEvent(pod, container, kubeContainerID.ID, v1.EventTypeWarning, events.FailedPostStartHook, "PostStartHook failed")
                        if err := m.killContainer(ctx, pod, kubeContainerID, container.Name, "FailedPostStartHook", reasonFailedPostStartHook, nil); err != nil {
                                klog.ErrorS(err, "Failed to kill container", "pod", klog.KObj(pod),
                                        "podUID", pod.UID, "containerName", container.Name, "containerID", kubeContainerID.String())
                        }
                        return msg, ErrPostStartHook
                }
        }
        //**********modified   
        n := necon.GetInstance()
        n.SetPID(containerID,pod.ObjectMeta.GetNamespace())


        fmt.Println("containerID : ",containerID)
        //************

        return "", nil
}

// generateContainerConfig generates container config for kubelet runtime v1.
func (m *kubeGenericRuntimeManager) generateContainerConfig(ctx context.Context, container *v1.Container, pod *v1.Pod, restartCount int, podIP, imageRef string, podIPs []string, nsTarget *kubecontainer.ContainerID) (*runtimeapi.ContainerConfig, func(), error) {
        opts, cleanupAction, err := m.runtimeHelper.GenerateRunContainerOptions(ctx, pod, container, podIP, podIPs)
        if err != nil {
                return nil, nil, err
        }

        uid, username, err := m.getImageUser(ctx, container.Image)
        if err != nil {
                return nil, cleanupAction, err
        }

        // Verify RunAsNonRoot. Non-root verification only supports numeric user.
        if err := verifyRunAsNonRoot(pod, container, uid, username); err != nil {
                return nil, cleanupAction, err
        }

        command, args := kubecontainer.ExpandContainerCommandAndArgs(container, opts.Envs)
        logDir := BuildContainerLogsDirectory(pod.Namespace, pod.Name, pod.UID, container.Name)
        err = m.osInterface.MkdirAll(logDir, 0755)
        if err != nil {
                return nil, cleanupAction, fmt.Errorf("create container log directory for container %s failed: %v", container.Name, err)
        }
        containerLogsPath := buildContainerLogsPath(container.Name, restartCount)
        restartCountUint32 := uint32(restartCount)
        config := &runtimeapi.ContainerConfig{
                Metadata: &runtimeapi.ContainerMetadata{
                        Name:    container.Name,
                        Attempt: restartCountUint32,
                },
                Image:       &runtimeapi.ImageSpec{Image: imageRef, UserSpecifiedImage: container.Image},
                Command:     command,
                Args:        args,
                WorkingDir:  container.WorkingDir,
                Labels:      newContainerLabels(container, pod),
                Annotations: newContainerAnnotations(container, pod, restartCount, opts),
                Devices:     makeDevices(opts),
                CDIDevices:  makeCDIDevices(opts),
                Mounts:      m.makeMounts(opts, container),
                LogPath:     containerLogsPath,
                Stdin:       container.Stdin,
                StdinOnce:   container.StdinOnce,
                Tty:         container.TTY,
        }

        // set platform specific configurations.
        if err := m.applyPlatformSpecificContainerConfig(config, container, pod, uid, username, nsTarget); err != nil {
                return nil, cleanupAction, err
        }

        // set environment variables
        envs := make([]*runtimeapi.KeyValue, len(opts.Envs))
        for idx := range opts.Envs {
                e := opts.Envs[idx]
                envs[idx] = &runtimeapi.KeyValue{
                        Key:   e.Name,
                        Value: e.Value,
                }
        }
        config.Envs = envs

        return config, cleanupAction, nil
}
