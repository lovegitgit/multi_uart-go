package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// ANSI Color Palette for distinct terminal visual identification
var ansiColors = []string{
	"\033[1;36m", // Cyan
	"\033[1;32m", // Green
	"\033[1;33m", // Yellow
	"\033[1;35m", // Magenta
	"\033[1;31m", // Red
	"\033[1;34m", // Blue
	"\033[1;96m", // Light Cyan
	"\033[1;92m", // Light Green
}

const colorReset = "\033[0m"

// SerialConfig stores port and baudrate pair
type SerialConfig struct {
	Port     string
	BaudRate int
	Alias    string
}

var (
	telnetClientsMutex sync.Mutex
	telnetClients      = make(map[net.Conn]bool)
	hexMode            bool
)

// LogMessage represents a framed log line from a specific port
type LogMessage struct {
	PortName  string
	Direction string // "RX", "TX", or "SYS"
	ColorCode string
	Timestamp time.Time
	Content   string
}

// MultiPortFlag allows parsing multiple -p arguments or space-separated lists
type MultiPortFlag []string

func (m *MultiPortFlag) String() string {
	return strings.Join(*m, " ")
}

func (m *MultiPortFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func main() {
	var portFlags MultiPortFlag
	var logFile string
	var listenAddr string
	var telnetUser string
	var telnetPass string
	var showFullDate bool
	var timeOnly bool
	var defaultBaud int

	flag.Var(&portFlags, "p", "串口配置 (单字母用 -p, 多字母可用 --port) 格式: COMx,Baud[,Alias] (如: -p COM25,115200,A1)")
	flag.Var(&portFlags, "port", "同 -p")
	flag.StringVar(&logFile, "o", "", "指定可选的输出保存日志文件名 (例如: -o serial_all.log)")
	flag.StringVar(&logFile, "out", "", "同 -o")
	flag.StringVar(&listenAddr, "l", "", "启动 Telnet 转发服务，格式: ip:port (例如: -l 0.0.0.0:8023)")
	flag.StringVar(&listenAddr, "listen", "", "同 -l")
	flag.StringVar(&telnetUser, "user", "", "Telnet 服务用户名 (如果不设置则无密码)")
	flag.StringVar(&telnetPass, "pass", "", "Telnet 服务密码")
	flag.BoolVar(&showFullDate, "full-date", false, "时间戳是否显示完整年份 (默认显示月-日)")
	flag.BoolVar(&timeOnly, "time-only", false, "时间戳仅显示时分秒微秒 (格式: HH:MM:SS.uuuuuu)")
	flag.IntVar(&defaultBaud, "b", 115200, "未指定波特率时的默认波特率")
	flag.IntVar(&defaultBaud, "baud", 115200, "同 -b")
	flag.BoolVar(&hexMode, "hex", false, "启用 Hex 模式 (收发数据以空格分隔的 16 进制显示/解析)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, " 🚀 高性能多串口实时日志汇总监控工具 (Multi-UART Logger)              \n")
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, "用法:\n")
		fmt.Fprintf(os.Stderr, "  %s -p COM23,115200 -p COM24,115200\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s --port COM23,115200 -l 0.0.0.0:8023 --user admin --pass 123456 --hex\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "参数说明:\n")
		fmt.Fprintf(os.Stderr, "  -p, --port string\n\t串口配置，格式: COMx,Baud[,Alias] (如: -p COM25,115200,A1)\n")
		fmt.Fprintf(os.Stderr, "  -l, --listen string\n\t启动 Telnet 转发服务，格式: ip:port (如: -l 0.0.0.0:8023)\n")
		fmt.Fprintf(os.Stderr, "  -o, --out string\n\t指定可选的输出保存日志文件名 (如: -o serial_log.txt)\n")
		fmt.Fprintf(os.Stderr, "  -b, --baud int\n\t为未指定波特率的串口提供默认波特率 (默认 115200)\n")
		fmt.Fprintf(os.Stderr, "  --user string\n\tTelnet 服务认证用户名 (不设置则无密码)\n")
		fmt.Fprintf(os.Stderr, "  --pass string\n\tTelnet 服务认证密码\n")
		fmt.Fprintf(os.Stderr, "  --hex\n\t启用 Hex 模式 (收发数据以空格分隔的 16 进制显示/解析)\n")
		fmt.Fprintf(os.Stderr, "  --full-date\n\t时间戳是否显示完整年份 (默认仅显示月-日)\n")
		fmt.Fprintf(os.Stderr, "  --time-only\n\t时间戳仅显示时分秒和毫秒 (格式: 15:04:05.000)\n")
	}

	flag.Parse()

	// Handle extra non-flag positional arguments as port configs (e.g. -p COM23,115200 COM24,115200)
	rawPortConfigs := []string(portFlags)
	
	// Golang's flag package stops parsing at the first non-flag argument.
	// We manually scan the remaining args to rescue incorrectly placed flags like -l or -o.
	args := flag.Args()
	var positionalPorts []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-l" || args[i] == "--listen" || args[i] == "-listen") && i+1 < len(args) {
			listenAddr = args[i+1]
			i++
		} else if (args[i] == "--user" || args[i] == "-user") && i+1 < len(args) {
			telnetUser = args[i+1]
			i++
		} else if (args[i] == "--pass" || args[i] == "-pass") && i+1 < len(args) {
			telnetPass = args[i+1]
			i++
		} else if (args[i] == "-o" || args[i] == "--out" || args[i] == "-out") && i+1 < len(args) {
			logFile = args[i+1]
			i++
		} else if args[i] == "--full-date" || args[i] == "-full-date" {
			showFullDate = true
		} else if args[i] == "--time-only" || args[i] == "-time-only" {
			timeOnly = true
		} else if args[i] == "--hex" || args[i] == "-hex" {
			hexMode = true
		} else if (args[i] == "-b" || args[i] == "--baud" || args[i] == "-baud") && i+1 < len(args) {
			if b, err := strconv.Atoi(args[i+1]); err == nil {
				defaultBaud = b
			}
			i++
		} else {
			positionalPorts = append(positionalPorts, args[i])
		}
	}
	rawPortConfigs = append(rawPortConfigs, positionalPorts...)

	configs := parseSerialConfigs(rawPortConfigs, defaultBaud)

	if len(configs) == 0 {
		fmt.Println("❌ 错误: 未指定有效的串口参数!")
		listAvailablePorts()
		flag.Usage()
		os.Exit(1)
	}

	// Prepare Log File writer if requested
	var logFileWriter *os.File
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("❌ 无法创建/打开日志文件 %s: %v", logFile, err)
		}
		logFileWriter = f
		defer logFileWriter.Close()
		fmt.Printf("📝 日志同步保存至: %s\n", logFile)
	}

	fmt.Printf("\n=======================================================================\n")
	fmt.Printf(" 🚀 正在启动多串口并发监控 (共 %d 个串口)\n", len(configs))
	for i, cfg := range configs {
		color := ansiColors[i%len(ansiColors)]
		aliasStr := ""
		if cfg.Alias != cfg.Port {
			aliasStr = fmt.Sprintf(" (Alias: %s)", cfg.Alias)
		}
		fmt.Printf("   [%d] %s%s%s%s | 波特率: %d\n", i+1, color, cfg.Port, colorReset, aliasStr, cfg.BaudRate)
	}
	fmt.Printf(" 💡 [时间戳格式] %s\n", getTimeFormatDesc(showFullDate, timeOnly))
	fmt.Printf(" 💡 [交互模式] 终端输入命令按回车可广播; 输入 Alias: cmd 或 COMx: cmd 可定向发送\n")
	fmt.Printf(" 💡 [系统指令] 输入 SYS: help 查看帮助\n")
	fmt.Printf("=======================================================================\n\n")

	logChan := make(chan LogMessage, 10000)
	var activePorts sync.Map // Port -> serial.Port

	// Calculate max name length for dynamic padding alignment
	maxNameLen := 0
	for _, cfg := range configs {
		if len(cfg.Alias) > maxNameLen {
			maxNameLen = len(cfg.Alias)
		}
	}
	if maxNameLen < 3 {
		maxNameLen = 3 // Ensure "SYS" lines align nicely
	}

	termFormat := fmt.Sprintf("%%s[%%-%ds]%%s[%%s] %%s%%s", maxNameLen)
	fileFormat := fmt.Sprintf("[%%-%ds][%%s] %%s%%s\n", maxNameLen)

	// 1. Start Serial Pipelines for each configured port
	for i, cfg := range configs {
		color := ansiColors[i%len(ansiColors)]
		go startPortPipeline(cfg.Port, cfg.Alias, cfg.BaudRate, color, logChan, &activePorts)
	}

	// 2. Start Console Interactive Command Reader (Broadcast or Target Command)
	go startStdinCommandReader(&activePorts, logChan)

	// 2.5 Start Telnet Server if requested
	if listenAddr != "" {
		go startTelnetServer(listenAddr, telnetUser, telnetPass, &activePorts, logChan)
	}

	// 3. Main Thread: Collect & Merge Output Loop
	var outMutex sync.Mutex
	for msg := range logChan {
		var timeStr string
		if timeOnly {
			timeStr = msg.Timestamp.Format("15:04:05.000000")
		} else if showFullDate {
			timeStr = msg.Timestamp.Format("2006-01-02 15:04:05.000000")
		} else {
			timeStr = msg.Timestamp.Format("01-02 15:04:05.000000") // Restored microsecond precision
		}

		var dirStrTerm, dirStrFile string
		if msg.Direction == "RX" {
			dirStrTerm = "\033[1;36m<<\033[0m "
			dirStrFile = "<< "
		} else if msg.Direction == "TX" {
			dirStrTerm = "\033[1;35m>>\033[0m " // Magenta for TX
			dirStrFile = ">> "
		} else {
			dirStrTerm = ""
			dirStrFile = ""
		}

		// COM port first format with dynamic alignment
		termLine := fmt.Sprintf(termFormat, msg.ColorCode, msg.PortName, colorReset, timeStr, dirStrTerm, msg.Content)

		outMutex.Lock()
		fmt.Println(termLine)

		// File Output (Plain text without ANSI color codes)
		if logFileWriter != nil {
			plainLine := fmt.Sprintf(fileFormat, msg.PortName, timeStr, dirStrFile, msg.Content)
			_, _ = logFileWriter.WriteString(plainLine)
		}

		// Telnet Output
		telnetClientsMutex.Lock()
		for conn := range telnetClients {
			_, err := conn.Write([]byte(termLine + "\r\n"))
			if err != nil {
				conn.Close()
				delete(telnetClients, conn)
			}
		}
		telnetClientsMutex.Unlock()

		outMutex.Unlock()
	}
}

// Parse input arguments like ["COM23,115200", "COM24,115200", "COM25,921600"]
func parseSerialConfigs(args []string, defaultBaud int) []SerialConfig {
	var results []SerialConfig
	seen := make(map[string]bool)

	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}

		parts := strings.Split(arg, ",")
		port := strings.TrimSpace(parts[0])
		if port == "" {
			continue
		}

		// Standardize port name casing (e.g., com23 -> COM23 on Windows)
		if strings.HasPrefix(strings.ToLower(port), "com") {
			port = strings.ToUpper(port)
		}

		baud := defaultBaud
		alias := port
		if len(parts) >= 2 {
			if b, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && b > 0 {
				baud = b
			}
		}
		if len(parts) >= 3 {
			a := strings.TrimSpace(parts[2])
			if a != "" {
				alias = a
			}
		}

		if !seen[port] {
			seen[port] = true
			results = append(results, SerialConfig{
				Port:     port,
				BaudRate: baud,
				Alias:    alias,
			})
		}
	}
	return results
}

// Dedicated port pipeline reading raw bytes and emitting framed LogMessages
func startPortPipeline(portName string, alias string, baudRate int, colorCode string, logChan chan<- LogMessage, activePorts *sync.Map) {
	mode := &serial.Mode{
		BaudRate: baudRate,
	}

	port, err := serial.Open(portName, mode)
	if err != nil {
		log.Printf("❌ [%s] 打开物理串口 %s 失败: %v", alias, portName, err)
		return
	}
	defer port.Close()

	activePorts.Store(alias, port)
	defer activePorts.Delete(alias)

	if alias == portName {
		log.Printf("✅ [%s] 串口连接成功 (%d baud)", alias, baudRate)
	} else {
		log.Printf("✅ [%s] 串口连接成功 (物理端口 %s, %d baud)", alias, portName, baudRate)
	}

	buf := make([]byte, 4096)
	var lineBuf bytes.Buffer

	// --- Hex Mode Timer-based Flush ---
	hexRxChan := make(chan []byte, 100)
	if hexMode {
		go func() {
			var hexBuf []byte
			timer := time.NewTimer(30 * time.Millisecond)
			if !timer.Stop() {
				<-timer.C
			}
			for {
				select {
				case chunk, ok := <-hexRxChan:
					if !ok {
						return // Port closed, exit
					}
					hexBuf = append(hexBuf, chunk...)
					timer.Reset(30 * time.Millisecond) // Flush after 30ms of silence
				case <-timer.C:
					if len(hexBuf) > 0 {
						var sb strings.Builder
						for j, b := range hexBuf {
							if j > 0 {
								sb.WriteByte(' ')
							}
							fmt.Fprintf(&sb, "%02X", b)
						}
						logChan <- LogMessage{
							PortName:  alias,
							Direction: "RX",
							ColorCode: colorCode,
							Timestamp: time.Now(),
							Content:   sb.String(),
						}
						hexBuf = hexBuf[:0]
					}
				}
			}
		}()
	}
	defer close(hexRxChan)
	// ----------------------------------

	for {
		n, err := port.Read(buf)
		if err != nil {
			log.Printf("⚠️ [%s] 串口读取异常断开: %v", alias, err)
			return
		}
		if n > 0 {
			now := time.Now()
			chunk := buf[:n]

			if hexMode {
				// Send chunk to our timeout-buffer instead of immediately printing
				hexRxChan <- append([]byte(nil), chunk...)
			} else {
				lineBuf.Write(chunk)

				for {
					lineBytes, err := lineBuf.ReadBytes('\n')
					if err != nil {
						// Put uncompleted line snippet back into buffer for next read
						lineBuf.Write(lineBytes)
						break
					}

					content := strings.TrimRight(string(lineBytes), "\r\n")
					if content != "" {
						logChan <- LogMessage{
							PortName:  alias,
							Direction: "RX",
							ColorCode: colorCode,
							Timestamp: now,
							Content:   content,
						}
					}
				}
			}
		}
	}
}

// Read stdin from interactive terminal for command broadcasting or targeted send
func startStdinCommandReader(activePorts *sync.Map, logChan chan<- LogMessage) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		// ANSI: Move cursor up 1 line and clear it. This erases the raw local echo!
		fmt.Print("\033[1A\033[2K")

		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		processInputCmd(text, activePorts, logChan)
	}
}

func processInputCmd(text string, activePorts *sync.Map, logChan chan<- LogMessage) {
	targetName, targetPort, cmdStr, isTargeted := parseTargetAndCommand(text, activePorts)

	if isTargeted && targetName == "SYS" {
		cmdLower := strings.ToLower(cmdStr)
		if cmdLower == "help" || cmdLower == "?" {
			printCommandHelp(logChan)
		} else {
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;31m",
				Timestamp: time.Now(),
				Content:   "❌ 未知的系统命令。支持: SYS: help",
			}
		}
		return
	}

	var cmdBytes []byte
	var err error

	if hexMode {
		// Remove spaces and parse hex
		cleanHex := strings.ReplaceAll(cmdStr, " ", "")
		cmdBytes, err = hex.DecodeString(cleanHex)
		if err != nil {
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;31m", // Red
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("❌ Hex 格式错误，忽略发送: %s", cmdStr),
			}
			return
		}
	} else {
		cmdBytes = []byte(cmdStr + "\r\n")
	}

	if isTargeted {
		_, err := targetPort.Write(cmdBytes)
		if err != nil {
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;31m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("❌ [发送失败 -> %s]: %v", targetName, err),
			}
		} else {
			logChan <- LogMessage{
				PortName:  targetName,
				Direction: "TX",
				ColorCode: "\033[1;35m", // Purple for TX
				Timestamp: time.Now(),
				Content:   cmdStr,
			}
		}
	} else {
		// Broadcast command to all active ports
		count := 0
		activePorts.Range(func(key, value any) bool {
			if p, ok := value.(serial.Port); ok {
				_, _ = p.Write(cmdBytes)
				count++
				portName := key.(string)
				logChan <- LogMessage{
					PortName:  portName,
					Direction: "TX",
					ColorCode: "\033[1;35m", // Purple for TX
					Timestamp: time.Now(),
					Content:   cmdStr,
				}
			}
			return true
		})
		
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;37m",
			Timestamp: time.Now(),
			Content:   fmt.Sprintf("📢 [广播命令 -> %d 个串口]: %s", count, cmdStr),
		}
	}
}

func startTelnetServer(addr string, user string, pass string, activePorts *sync.Map, logChan chan<- LogMessage) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("❌ 无法启动 Telnet 服务: %v", err)
	}
	logChan <- LogMessage{
		PortName:  "SYS",
		Direction: "SYS",
		ColorCode: "\033[1;37m",
		Timestamp: time.Now(),
		Content:   fmt.Sprintf("🌐 Telnet 服务已启动，监听地址: %s", addr),
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go handleTelnetClient(conn, user, pass, activePorts, logChan)
	}
}

func handleTelnetClient(conn net.Conn, user string, pass string, activePorts *sync.Map, logChan chan<- LogMessage) {
	var authenticated bool

	defer func() {
		if authenticated {
			telnetClientsMutex.Lock()
			delete(telnetClients, conn)
			telnetClientsMutex.Unlock()
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;37m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("💔 Telnet 客户端断开: %s", conn.RemoteAddr()),
			}
		}
		conn.Close()
	}()

	scanner := bufio.NewScanner(conn)

	// Handle Authentication
	if user != "" || pass != "" {
		if user != "" {
			conn.Write([]byte("Username: "))
			if !scanner.Scan() {
				return
			}
			inputUser := strings.TrimSpace(strings.TrimRight(scanner.Text(), "\r\x00"))
			if inputUser != user {
				conn.Write([]byte("\033[1;31mAuthentication failed.\033[0m\r\n"))
				return
			}
		}

		if pass != "" {
			conn.Write([]byte("Password: "))
			if !scanner.Scan() {
				return
			}
			inputPass := strings.TrimSpace(strings.TrimRight(scanner.Text(), "\r\x00"))
			if inputPass != pass {
				conn.Write([]byte("\033[1;31mAuthentication failed.\033[0m\r\n"))
				return
			}
		}
	}

	authenticated = true
	telnetClientsMutex.Lock()
	telnetClients[conn] = true
	telnetClientsMutex.Unlock()

	logChan <- LogMessage{
		PortName:  "SYS",
		Direction: "SYS",
		ColorCode: "\033[1;37m",
		Timestamp: time.Now(),
		Content:   fmt.Sprintf("🔗 新的 Telnet 客户端连接成功: %s", conn.RemoteAddr()),
	}

	conn.Write([]byte("\033[1;32m欢迎进入多串口 Telnet 控制台！输入 help 查看帮助。\033[0m\r\n"))

	for scanner.Scan() {
		// ANSI: Move cursor up 1 line and clear it to erase the telnet client's local echo
		conn.Write([]byte("\033[1A\033[2K"))

		// Clean up common telnet character artifacts (e.g. \r or \x00)
		text := strings.TrimRight(scanner.Text(), "\r\x00")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		processInputCmd(text, activePorts, logChan)
	}
}

// Parse commands like "62-reset", "COM62: reset", "com62 reset" or broadcast "reset"
func parseTargetAndCommand(input string, activePorts *sync.Map) (targetName string, port serial.Port, cmdStr string, isTargeted bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil, "", false
	}

	var prefix string
	var cmd string

	// 1. Check hyphen format (e.g. "62-reset", "com62-reset", "COM62-factoryreset")
	if idx := strings.Index(input, "-"); idx > 0 {
		possiblePort := strings.TrimSpace(input[:idx])
		if isNumericOrPortName(possiblePort) {
			prefix = possiblePort
			cmd = strings.TrimSpace(input[idx+1:])
		}
	}

	// 2. Check colon format (e.g. "62: reset", "COM62: reset")
	if prefix == "" {
		if idx := strings.Index(input, ":"); idx > 0 {
			possiblePort := strings.TrimSpace(input[:idx])
			if isNumericOrPortName(possiblePort) {
				prefix = possiblePort
				cmd = strings.TrimSpace(input[idx+1:])
			}
		}
	}

	// 3. Check space format (e.g. "62 reset", "com62 reset")
	if prefix == "" {
		parts := strings.Fields(input)
		if len(parts) >= 2 && isNumericOrPortName(parts[0]) {
			prefix = parts[0]
			cmd = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
	}

	if prefix != "" && cmd != "" {
		if strings.ToUpper(prefix) == "SYS" {
			return "SYS", nil, cmd, true
		}
		matchedName, matchedPort := matchActivePort(prefix, activePorts)
		if matchedPort != nil {
			return matchedName, matchedPort, cmd, true
		}
	}

	return "", nil, input, false
}

// Check if a string is numeric (e.g. "62") or port name (e.g. "com62", "COM62") or alias
func isNumericOrPortName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Check if string contains only alphanumeric characters (valid alias/port)
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Match prefix with active connected serial ports
func matchActivePort(prefix string, activePorts *sync.Map) (string, serial.Port) {
	prefixUpper := strings.ToUpper(strings.TrimSpace(prefix))

	var matchedName string
	var matchedPort serial.Port

	activePorts.Range(func(key, value any) bool {
		portName, okKey := key.(string)
		portObj, okVal := value.(serial.Port)
		if !okKey || !okVal {
			return true
		}

		portNameUpper := strings.ToUpper(portName)

		// 1. Exact match (e.g., "COM62" == "COM62")
		if portNameUpper == prefixUpper {
			matchedName = portName
			matchedPort = portObj
			return false
		}

		// 2. Numeric suffix match (e.g., prefix "62" matches "COM62")
		if strings.HasSuffix(portNameUpper, prefixUpper) || strings.Contains(portNameUpper, prefixUpper) {
			matchedName = portName
			matchedPort = portObj
			return false
		}

		return true
	})

	return matchedName, matchedPort
}

func printCommandHelp(logChan chan<- LogMessage) {
	lines := []string{
		"-----------------------------------------------------------------------",
		"💡 多串口终端交互命令输入指南:",
		"  1. 定向发送 (推荐格式):",
		"     - 端口简写横杠: 62-reset        (向 COM62 发送 reset)",
		"     - 端口冒号格式: COM62: reset    (向 COM62 发送 reset)",
		"     - 端口别名格式: A1: reset       (向别名为 A1 的端口发送 reset)",
		"  2. 全局广播 (无端口前缀):",
		"     - 直接输入命令: reset           (向所有打开的串口广播发送 reset)",
		"  3. 系统指令 (SYS 前缀):",
		"     - 查看帮助: SYS: help",
		"-----------------------------------------------------------------------",
	}
	
	now := time.Now()
	for _, line := range lines {
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;37m",
			Timestamp: now,
			Content:   line,
		}
	}
}

func listAvailablePorts() {
	ports, err := serial.GetPortsList()
	if err != nil || len(ports) == 0 {
		fmt.Println("ℹ️ 未找到任何可用的串口!")
		return
	}
	fmt.Println("🔍 当前可用串口列表:")
	for _, p := range ports {
		fmt.Printf("   - %s\n", p)
	}
}

func getTimeFormatDesc(showFullDate bool, timeOnly bool) string {
	if timeOnly {
		return "仅时间 [HH:MM:SS.uuuuuu]"
	}
	if showFullDate {
		return "完整日期 [YYYY-MM-DD HH:MM:SS.uuuuuu]"
	}
	return "推荐精简日期 [MM-DD HH:MM:SS.uuuuuu]"
}
