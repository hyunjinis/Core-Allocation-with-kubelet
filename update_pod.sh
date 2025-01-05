#!/bin/bash

# bounded_cores 실행 및 결과 정리
bounded_cores=$(./bounded_core_remote.sh | awk -F': ' '{print $2}' | xargs)
if [ -z "$bounded_cores" ]; then
    echo "No bounded cores found"
    exit 1
fi
echo "Bounded cores: $bounded_cores"

# 입력 및 출력 YAML 파일 설정
input_yaml="p1_cgroup.yaml"
output_yaml="p1-updated.yaml"
cp "$input_yaml" "$output_yaml"

# bounded_cores를 배열로 변환
bounded_core_array=($bounded_cores)

# 첫 번째 값과 두 번째 값을 추출
slo1=${bounded_core_array[0]:-0}  # 첫 번째 값 (기본값: 0)
slo2=${bounded_core_array[1]:-0}  # 두 번째 값 (기본값: 0)

# YAML 업데이트
yq -i "
  .spec.containers[0].resources.limits.\"example.com/SLO1\" = \"$slo1\" |
  .spec.containers[0].resources.limits.\"example.com/SLO2\" = \"$slo2\"
" "$output_yaml"

echo "Updated YAML file saved to $output_yaml"

