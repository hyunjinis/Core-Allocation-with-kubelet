package necon

import(
        "k8s.io/api/core/v1"
//      "k8s.io/apimachinery/pkg/api/resource"
        "os"
        "io/ioutil"
//      "os/exec"
        "fmt"
        "strconv"
        "sync"
        "strings"
        "k8s.io/klog/v2"
)

type necon struct {
        path string
        pod v1.Pod
        count int
}

var(
        n *necon
        once sync.Once
)


func GetInstance() *necon{
        once.Do(func() {
                n = &necon{}
        })
        return n
}

func (nec *necon) SetNeconPod(pod v1.Pod) error{
        namespace := pod.ObjectMeta.GetNamespace()
        if namespace != "kube-system" {
                nec.pod = pod
                nec.count++
        }
        return nil
}
/*
func (nec *necon) GetNeconPod() *v1.Pod{
        return nec.pod
}
*/
func (nec *necon) SetCgroupPath(path string) error{
        nec.path = path

        return nil
}

func (nec *necon) SetPID(containerID string,namespace string) error{

        if namespace != "kube-system" {
                //w,_ := exec.Command("docker","inspect","--format='{{.State.Pid}}'",containerID).Output()
                //위 코드가 문제인듯
                fmt.Println("%s",nec.path+"/"+containerID+"/cgroup.procs")
                pid,err:= ioutil.ReadFile(fmt.Sprintf("%s",nec.path+"/"+containerID+"/cgroup.procs"))
                err = ioutil.WriteFile(fmt.Sprintf("/proc/oslab/vif%d/pid",nec.count),[]byte(pid),0)
                if err != nil{
                        fmt.Println("pid write err!")
                }
        }
        return nil
}

func (nec *necon) ApplyCPUAffinity(pod v1.Pod) error {
        klog.Infof("ApplyCPUAffinity 함수가 호출되었습니다. Pod UID: %s", pod.ObjectMeta.UID)

        sloField1, exists := pod.Spec.Containers[0].Resources.Limits["example.com/SLO1"]
        if !exists {
                return fmt.Errorf("SLO1 필드가 존재하지 않습니다: Pod UID: %s", pod.ObjectMeta.UID)
        }

        sloField2, exists2 := pod.Spec.Containers[0].Resources.Limits["example.com/SLO2"]
        if !exists2 {
                return fmt.Errorf("SLO2 필드가 존재하지 않습니다: Pod UID: %s", pod.ObjectMeta.UID)
        }

        coreToExclude1, err := strconv.Atoi(sloField1.String())
        if err != nil {
                return fmt.Errorf("SLO1 필드 변환 오류: %v", err)
        }

        coreToExclude2, err := strconv.Atoi(sloField2.String())
        if err != nil {
                return fmt.Errorf("SLO2 필드 변환 오류: %v", err)
        }

        // 전체 코어 목록에서 제외할 코어 제거
        allCores := []int{0, 1, 2, 3} // 예시로 4개 코어가 있다고 가정
        allowedCores := []string{}
        for _, core := range allCores {
                if core != coreToExclude1 && core != coreToExclude2 {
                allowedCores = append(allowedCores, strconv.Itoa(core))
                }
        }
        cgroupCompatiblePodUID := strings.ReplaceAll(string(pod.ObjectMeta.UID), "-", "_")

        cgroupPath := fmt.Sprintf("/sys/fs/cgroup/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod%s.slice/cpuset.cpus", cgroupCompatiblePodUID)
        klog.Infof("Pod UID: %s\n", pod.ObjectMeta.UID) // Pod UID 확인용 로그
        klog.Infof("CGroup Path: %s\n", cgroupPath)     // CGroup 경로 확인용 로그
        klog.Infof("Allowed Cores: %s", strings.Join(allowedCores, ","))
        if _, err := os.Stat(cgroupPath); os.IsNotExist(err) {
                return fmt.Errorf("CGroup 경로가 존재하지 않음: %s", cgroupPath)
        }

        err = os.WriteFile(cgroupPath, []byte(strings.Join(allowedCores, ",")), 0644)
        if err != nil {
                return fmt.Errorf("CGroup 설정 파일 업데이트 오류: %v", err)
        }
        klog.Infof("CGroup 파일 %s 업데이트 완료", cgroupPath)

    return nil

}
