# Core-Allocation-with-kubelet

Goal : 'Interrupt Affinity Core' Repository에서 실행한 bounded_core 스크립트를 쿠버네티스 설정으로 적용시키기

먼저 1. cpuset 개념을 통해 core를 할당
2. cpuset을 kubelet과 연동시켜 쿠버네티스 설정으로 적용시킴(CPU Manager Policy 사용)
3. 단 이때 'kubectl describe nodes --- ' 명령어 적용 시 cpu core 할당에 대한 정보가 떴으면 한다.
