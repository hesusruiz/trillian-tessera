#!/bin/bash
go run ./internal/hammer \
  --log_public_key=example.com/log/testdata+33d7b496+AeHTu4Q3hEIMHNqc6fASMsq3rKNx280NI+oO5xCFkkSx \
  --log_url=http://localhost:2024/ \
  --max_read_ops=0 \
  --num_writers=1500 \
  --max_write_ops=50000 \
  --max_runtime=1m \
  --leaf_write_goal=1024000
