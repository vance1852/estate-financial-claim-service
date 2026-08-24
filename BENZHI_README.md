# BENZHI_README

这是一个处理遗产金融账户查询与小额存款领取的 Go 后端服务，支持案件审核、账户核验和受控支付流程。

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-338-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-338-arm64 linux/arm64
docker run -it benzhi-task-338-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-338-arm64:latest
```
