Core-Allocation-with-kubelet    
[kubelet 설정 변경을 통한 cpuset 설정 자동화]
==========================================================================

**"bounded_core.sh"와 "interrupt.sh"를 통한 CPU Core 설정 방법의 한계점**
 - taskset은 스크립트 실행 시점에만 적용
    - 새로운 Pod 생성 시 설정 일관성 X
 - Worker node마다 Affinity 설정 다름
    - 항상 스크립트 실행을 통한 Bounding Core 확인 필요

위의 한계점에 대한 해결방안으로 kubernetes kubelet의 설정 자체를 변경하여 새로운 pod 생성 시 cpuset 설정이 자동화 되도록하는 방법을 내놓음

**Depcon 스케줄링 방식을 참고하여 새로운 necon 파일 작성**
- kubernetes/pkg/kubelet/necon/necon.go
    - 주요 함수(ApplyCPUAffinity 함수) :   
      'example.com/SLO1' 항목과 'example.com/SLO2' 항목을 pod yaml 파일에서 받아와 정수로 변환    
      전체 코어에서 두 항목에 입력된 코어 번호를 제외한 나머지 코어들을 allowedCores로 분류
      새로 생성된 Pod의 CgroupPath를 가져온 후 위의 내용들의 디버깅 목적용 확인 메세지 출력
- kubernetes/pkg/kubelet/kubelet.go
    - Syncpod 함수 속 Cgroup 생성, Update 하는 부분에서 파드에 대한 instance를 가져와 소스코드 파일로 전달
- kubernetes/pkg/kubelet/kuberuntime/kuberuntime_container.go
    - Pod 내부의 컨테이너에 대한 정보(PID)을 necon 설정 파일로 전달
- kubernetes/pkg/kubelet/kuberuntime/kuberuntime_manager.go

- kubernetes/pkg/kubelet/cm/cgroup_manager_linux.go
    - Cgroup path & PID 정보를 소스코드 파일로 전달




