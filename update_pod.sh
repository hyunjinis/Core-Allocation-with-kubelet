#!/bin/bash

bounded_cores=$(./bounded_core.sh)
if [ -z "$bounded_cores" ]; then
    echo "No bounded cores found"
    exit 1
fi
echo "Bounded cores: $bounded_cores"

input_yaml="p1.yaml"
output_yaml="p1-updated.yaml"
cp "$input_yaml" "$output_yaml"

# 3. example.com/SLO 필드 업데이트
bounded_core_string=$(echo "$bounded_cores" | tr ' ' ',')
yq eval ".spec.containers[0].resources.limits.\"example.com/SLO\" = \"$bounded_core_string\"" -i "$output_yaml"

echo "Updated YAML file saved to $output_yaml"
