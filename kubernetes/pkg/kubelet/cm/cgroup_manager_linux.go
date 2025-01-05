func (m *cgroupManagerImpl) Update(cgroupConfig *CgroupConfig) error {
        start := time.Now()
        defer func() {
                metrics.CgroupManagerDuration.WithLabelValues("update").Observe(metrics.SinceInSeconds(start))
        }()
        //*********modified
        resourceConfig := cgroupConfig.ResourceParameters
        resources := m.toResources(resourceConfig)

        cgroupPaths := m.buildCgroupPaths(cgroupConfig.Name)

        libcontainerCgroupConfig := &libcontainerconfigs.Cgroup{
                Resources: resources,
        }
        //********

        libcontainerCgroupConfig = m.libctCgroupConfig(cgroupConfig, true)
        manager, err := manager.New(libcontainerCgroupConfig)
        if err != nil {
                return fmt.Errorf("failed to create cgroup manager: %v", err)
        }
        //*********modified
        n := necon.GetInstance()
        n.SetCgroupPath(cgroupPaths["cpu"])
        //***********

        return manager.Set(libcontainerCgroupConfig.Resources)
}
