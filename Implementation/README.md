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
- 주요 함수(ApplyCPUAffinity 함수) 
