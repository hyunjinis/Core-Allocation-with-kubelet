#!/bin/bash

limit=10000

bounded_core() {
    cores=$(nproc)
    bounded=""

    interrupt_number=$(grep eth0 /proc/interrupts | awk -F'[: ]+' '{print $1,$2}' | tr -s '\n' ' ')

    for num in $interrupt_number; do
            cores=$(nproc)
                   for (( i=0; i<$cores; i++ )); do
                        core=$((i+2))
                        amount=$(awk -v num="$num" -v core="$core" '$1 ~ num":" {print $core}' /proc/interrupts)
                        if [ ! -z "$amount" ] && [ "$amount" -gt "$limit" ]; then
                                bounded="$bounded $i"
                                break
                        fi
                   done
    done
    echo "Bounded cores: $bounded"
}

result=$(bounded_core)

bounded=$(echo "$result" | awk '/Bounded cores:/ {for (i=3; i<=NF; i++) print $i}' | xargs)
others=""

total=$(nproc)

for core in $(seq 0 $((total-1))); do
    if [[ ! " $bounded " =~ " $core " ]]; then
        if [ -z "$others" ]; then
            others="$core"
        else
            others="$others,$core"
        fi
    fi
done

others=$(echo "$others" | tr -d ' ' | sed 's/,/, /g')

echo "Bounded cores: $bounded"
echo "Other cores: $others"

#cpuset="/sys/fs/cgroup/cpuset/my_cpuset"
#echo "$others" > $cpuset/cpus
#echo "0" > $cpuset/mems

core_count=$(echo "$bounded" | tr -cd ' ' | wc -c)
core_count=$((core_count+1))

echo "Number of bounded cores: $CORE_COUNT"
