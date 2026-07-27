# Multi-UART Logger (高性能多串口实时日志汇总监控工具)

Multi-UART Logger 是一款使用 Go 语言开发的轻量级、跨平台、高性能的串口监控工具。它专为硬件工程师、嵌入式开发人员和需要同时监控多个外围设备（如蓝牙节点、传感器网络等）的人员设计。

## 🌟 核心功能特性 (Features)

- **多并发串口监控**：支持同时打开任意数量的物理或虚拟串口，将所有日志集中汇总输出到一个控制台，方便排查时序问题和多设备同步通信。
- **智能彩色高亮**：为每个串口自动分配不同的 ANSI 颜色标签，在密集滚动的日志流中也能极速分辨数据的来源。
- **网络 Telnet 转发与远程调试**：内置支持身份认证（账号/密码）的网络 Telnet 服务。你可以将测距日志通过 Tailscale、ZeroTier 或局域网实时共享给远端调试人员。
- **Hex (16进制) 模式收发与断帧自愈**：
  - 自动将串口二进制流格式化为 `DE AD BE EF`。
  - 内置基于**30ms静默超时 (Inter-Byte Timeout)** 缓冲机制，完美解决高速连续二进制流常见的终端断行、分片（Fragmentation）显示问题。
  - 支持在控制台直接输入 `5A A7 00` 发送 Hex 报文。
- **统一灵活的交互命令行**：既能对所有连接的串口一键“全局广播”发送命令，又能指定单独的设备进行精准下发（如 `62-reset` 或 `COM62: reset`）。
- **文件持久化**：一键同步将所有日志以去色后的纯文本格式追加写入本地磁盘日志文件。

---

## 🚀 快速使用指南 (Usage)

你可以通过命令行传入一个或多个串口配置，形如 `-p 串口名称,波特率`。

```bash
# 基本用法：同时监听两个串口，波特率均为 115200
./multi_uart_logger -p COM23,115200 -p COM24,115200

# 开启网络转发并设置账号密码保护
./multi_uart_logger -p COM25,115200 --listen 0.0.0.0:12345 --user admin --pass 123456

# 开启 Hex 数据模式，并将聚合日志保存到 serial_log.txt
./multi_uart_logger -p COM26,921600 --hex --out serial_log.txt
```

### 命令说明
你可以使用 `help` 或 `--help` 随时查看所有参数：
* `-p` 或 `--port` : 串口配置参数。如 `COMx,Baud` 或 `/dev/ttyUSB0,115200`
* `-l` 或 `--listen`: 启动 Telnet 监听服务的 `IP:Port` 
* `--user` : Telnet 登录认证用户名 (留空则无密码)
* `--pass` : Telnet 登录认证密码
* `--hex` : 启用全包聚合后的 16 进制收发模式
* `-o` 或 `--out` : 日志输出文件保存路径
* `-b` 或 `--baud` : 为没有提供波特率的串口设置全局默认波特率 (默认 115200)

---

## 🛠️ 源码编译教程 (Build Instructions)

本项目采用 Go (Golang) 编写，天生支持极简的跨平台交叉编译，没有任何第三方 C 语言依赖库绑定。

### 1. 准备环境
首先请确保你的系统中已安装 [Go 运行环境 (v1.18+)](https://go.dev/dl/)。
检查 Go 是否可用：
```bash
go version
```

### 2. 初始化与下载依赖
在项目根目录（`multi_uart_logger.go` 所在目录），首先执行以下命令下载所需的串口依赖包：
```bash
go mod tidy
```

### 3. 针对不同操作系统的编译命令

**A. 编译为 Windows 可执行文件 (.exe)**
无论你当前是在 Linux, macOS 还是 Windows，均可生成 Windows 程序：
```bash
GOOS=windows GOARCH=amd64 go build -o multi_uart_logger.exe multi_uart_logger.go
```

**B. 编译为 Linux 可执行文件**
适用于标准的 Ubuntu / Debian / CentOS 服务器或桌面版：
```bash
GOOS=linux GOARCH=amd64 go build -o multi_uart_logger multi_uart_logger.go
```
*提示：如果是树莓派或 ARM 架构开发板，请将 `GOARCH=amd64` 改为 `GOARCH=arm64`。*

**C. 编译为 macOS (Intel/M系列)**
```bash
# 适用于 M1/M2/M3 等 Apple Silicon 芯片：
GOOS=darwin GOARCH=arm64 go build -o multi_uart_logger_mac multi_uart_logger.go

# 适用于老款 Intel Mac 芯片：
GOOS=darwin GOARCH=amd64 go build -o multi_uart_logger_mac multi_uart_logger.go
```

编译完成后，同目录下即会生成轻量的独立可执行文件，**你无需在此目标机器上安装任何依赖环境（包括 Go）即可直接运行此程序！**
