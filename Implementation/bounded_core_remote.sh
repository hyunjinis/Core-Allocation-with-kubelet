limit=10000

bounded_core_remote() {
    # 원격 서버 정보
    remote_host="192.168.0.252"
    remote_user="ubuntu4"

    # 원격 명령 실행
    cores=$(ssh ${remote_user}@${remote_host} "nproc")
    bounded=""

    interrupt_number=$(ssh ${remote_user}@${remote_host} "grep eth0 /proc/interrupts | awk -F'[: ]+' '{print \$1,\$2}' | tr -s '\n' ' '")

    for num in $interrupt_number; do
        for (( i=0; i<$cores; i++ )); do
            core=$((i+2))
            amount=$(ssh ${remote_user}@${remote_host} "awk -v num=\"$num\" -v core=\"$core\" '\$1 ~ num\":\" {print \$core}' /proc/interrupts")
            if [ ! -z "$amount" ] && [ "$amount" -gt "$limit" ]; then
                bounded="$bounded $i"
                break
            fi
        done
    done

    echo "Bounded cores on $remote_host: $bounded"
}

bounded_core_remote
