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
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"golang.org/x/term"
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
	HexMode  bool
	EOL      string
}

var (
	telnetClientsMutex sync.Mutex
	telnetClients      = make(map[net.Conn]bool)
	hexMode            bool
	portHexModes       sync.Map // lower(port/alias) -> bool
	globalEOL          = "\r\n"
	portEOLModes       sync.Map // lower(port/alias) -> string

	origTerminalState *term.State
)

func isPortHexMode(name string) bool {
	if v, ok := portHexModes.Load(strings.ToLower(name)); ok {
		return v.(bool)
	}
	return hexMode
}

func getPortEOL(name string) string {
	if v, ok := portEOLModes.Load(strings.ToLower(name)); ok {
		return v.(string)
	}
	return globalEOL
}

func parseEOL(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crlf", "\\r\\n", "\r\n":
		return "\r\n", true
	case "lf", "\\n", "\n":
		return "\n", true
	case "cr", "\\r", "\r":
		return "\r", true
	case "none", "null", "empty", "no", "0":
		return "", true
	default:
		return "", false
	}
}

func formatEOLDesc(eol string) string {
	switch eol {
	case "\r\n":
		return "CRLF (\\r\\n)"
	case "\n":
		return "LF (\\n)"
	case "\r":
		return "CR (\\r)"
	case "":
		return "NONE (无)"
	default:
		return fmt.Sprintf("%q", eol)
	}
}

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
	var listPorts bool
	var telnetUser string
	var telnetPass string
	var showFullDate bool
	var timeOnly bool
	var defaultBaud int
	var eolFlag string

	flag.Var(&portFlags, "p", "串口配置 (单字母用 -p, 多字母可用 --port) 格式: COMx[,Baud[,Alias[,Mode[,EOL]]]] (如: -p COM25,115200,A1,text,lf 或留空覆盖: -p COM5,,,,cr)")
	flag.Var(&portFlags, "port", "同 -p")
	flag.StringVar(&logFile, "o", "", "指定可选的输出保存日志文件名 (例如: -o serial_all.log)")
	flag.StringVar(&logFile, "out", "", "同 -o")
	flag.BoolVar(&listPorts, "l", false, "列出当前系统所有可用串口并退出")
	flag.BoolVar(&listPorts, "list", false, "同 -l")
	flag.StringVar(&listenAddr, "L", "", "启动 Telnet 转发服务，格式: ip:port (例如: -L 0.0.0.0:8023)")
	flag.StringVar(&listenAddr, "listen", "", "同 -L")
	flag.StringVar(&telnetUser, "user", "", "Telnet 服务用户名 (如果不设置则无密码)")
	flag.StringVar(&telnetPass, "pass", "", "Telnet 服务密码")
	flag.BoolVar(&showFullDate, "full-date", false, "时间戳是否显示完整年份 (默认显示月-日)")
	flag.BoolVar(&timeOnly, "time-only", false, "时间戳仅显示时分秒微秒 (格式: HH:MM:SS.uuuuuu)")
	flag.IntVar(&defaultBaud, "b", 115200, "未指定波特率时的默认波特率")
	flag.IntVar(&defaultBaud, "baud", 115200, "同 -b")
	flag.BoolVar(&hexMode, "hex", false, "启用全局默认 Hex 模式 (收发数据以空格分隔的 16 进制显示/解析)")
	flag.StringVar(&eolFlag, "eol", "crlf", "全局文本模式发送行尾换行符: crlf (默认), lf, cr, none")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, " 🚀 高性能多串口实时日志汇总监控工具 (Multi-UART Logger)              \n")
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, "用法:\n")
		fmt.Fprintf(os.Stderr, "  %s --list\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM23,115200 -p COM24,115200\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM3 COM4 COM5 --hex -p COM3,,,text\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM5,,,,cr -p COM6,115200,,text,lf --eol crlf\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s --port COM23,115200 --listen 0.0.0.0:8023 --user admin --pass 123456 --hex\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "参数说明:\n")
		fmt.Fprintf(os.Stderr, "  -p, --port string\n\t串口配置，格式: COMx[,Baud[,Alias[,Mode[,EOL]]]] (如: -p COM25,115200,A1,text,lf 或留空覆盖: -p COM5,,,,cr)\n")
		fmt.Fprintf(os.Stderr, "  --eol string\n\t全局文本模式发送行尾换行符: crlf (默认), lf, cr, none\n")
		fmt.Fprintf(os.Stderr, "  -l, --list\n\t列出当前系统所有可用串口并退出\n")
		fmt.Fprintf(os.Stderr, "  -L, --listen string\n\t启动 Telnet 转发服务，格式: ip:port (如: --listen 0.0.0.0:8023)\n")
		fmt.Fprintf(os.Stderr, "  -o, --out string\n\t指定可选的输出保存日志文件名 (如: -o serial_log.txt)\n")
		fmt.Fprintf(os.Stderr, "  -b, --baud int\n\t为未指定波特率的串口提供默认波特率 (默认 115200)\n")
		fmt.Fprintf(os.Stderr, "  --user string\n\tTelnet 服务认证用户名 (不设置则无密码)\n")
		fmt.Fprintf(os.Stderr, "  --pass string\n\tTelnet 服务认证密码\n")
		fmt.Fprintf(os.Stderr, "  --hex\n\t启用全局默认 Hex 模式 (收发数据以空格分隔的 16 进制显示/解析)\n")
		fmt.Fprintf(os.Stderr, "  --full-date\n\t时间戳是否显示完整年份 (默认仅显示月-日)\n")
		fmt.Fprintf(os.Stderr, "  --time-only\n\t时间戳仅显示时分秒和毫秒 (格式: 15:04:05.000)\n")
	}

	flag.Parse()

	// Handle extra non-flag positional arguments as port configs (e.g. -p COM23,115200 COM24,115200)
	rawPortConfigs := []string(portFlags)

	// Golang's flag package stops parsing at the first non-flag argument.
	// We manually scan the remaining args to rescue flags placed after a positional port.
	args := flag.Args()
	var positionalPorts []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-l" || args[i] == "--list" || args[i] == "-list" {
			listPorts = true
		} else if (args[i] == "-L" || args[i] == "--listen" || args[i] == "-listen") && i+1 < len(args) {
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
		} else if (args[i] == "--eol" || args[i] == "-eol") && i+1 < len(args) {
			eolFlag = args[i+1]
			i++
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

	if listPorts {
		listAvailablePorts()
		return
	}

	if parsedEOL, ok := parseEOL(eolFlag); ok {
		globalEOL = parsedEOL
	}

	configs := parseSerialConfigs(rawPortConfigs, defaultBaud, hexMode, globalEOL)

	for _, cfg := range configs {
		portHexModes.Store(strings.ToLower(cfg.Port), cfg.HexMode)
		portHexModes.Store(strings.ToLower(cfg.Alias), cfg.HexMode)
		portEOLModes.Store(strings.ToLower(cfg.Port), cfg.EOL)
		portEOLModes.Store(strings.ToLower(cfg.Alias), cfg.EOL)
	}

	if len(configs) == 0 {
		fmt.Println("❌ 错误: 未指定有效的串口参数!")
		listAvailablePorts()
		flag.Usage()
		os.Exit(1)
	}

	if st, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		origTerminalState = st
		defer term.Restore(int(os.Stdin.Fd()), st)
	}

	// Prepare Log File writer if requested
	var logFileWriter *os.File
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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
		modeTag := "\033[1;36m[TEXT]\033[0m"
		if cfg.HexMode {
			modeTag = "\033[1;33m[HEX]\033[0m"
		}
		fmt.Printf("   [%d] %s%s%s%s | 波特率: %d | 模式: %s | EOL: %s\n", i+1, color, cfg.Port, colorReset, aliasStr, cfg.BaudRate, modeTag, formatEOLDesc(cfg.EOL))
	}
	fmt.Printf(" 💡 [时间戳格式] %s\n", getTimeFormatDesc(showFullDate, timeOnly))
	fmt.Printf(" 💡 [发送换行符] 全局默认: %s (可用 --eol 设置全局, 或 -p COMx,,,,<eol> 单独覆盖)\n", formatEOLDesc(globalEOL))
	fmt.Printf(" 💡 [交互模式] 终端输入命令按回车可广播; 输入 Alias: cmd 或 COMx: cmd 可定向发送\n")
	fmt.Printf(" 💡 [退出程序] 按 Ctrl+] 退出 multi_uart_logger\n")
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
		go startPortPipeline(cfg.Port, cfg.Alias, cfg.BaudRate, cfg.HexMode, color, logChan, &activePorts)
	}

	// 2. Start Console Interactive Command Reader (Broadcast or Target Command)
	go startStdinCommandReader(&activePorts, logChan)

	// Trap SIGINT (Ctrl+C) and forward 0x03 to active UART devices instead of exiting
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		for sig := range sigChan {
			ctrlBytes := []byte{0x03}
			sigName := "Ctrl+C (SIGINT)"
			if sig != os.Interrupt {
				ctrlBytes = []byte{0x1C}
				sigName = "SIGTERM"
			}
			count := 0
			activePorts.Range(func(key, value any) bool {
				if p, ok := value.(serial.Port); ok {
					_, _ = p.Write(ctrlBytes)
					count++
				}
				return true
			})
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;35m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("📢 [捕获系统信号 %s] 已向 %d 个串口透传广播下发 0x%02X。退出程序请输入 'exit' 或 'q'", sigName, count, ctrlBytes[0]),
			}
		}
	}()

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



// Parse input arguments like ["COM23,115200", "COM24,115200", "COM25,921600,A1,hex", "COM5,,,,cr"]
func parseSerialConfigs(args []string, defaultBaud int, globalHex bool, defaultEOL string) []SerialConfig {
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
		isHex := globalHex
		eol := defaultEOL

		for idx, part := range parts[1:] {
			p := strings.TrimSpace(part)
			if p == "" {
				continue // empty slot: preserve default/inherited value
			}
			pLower := strings.ToLower(p)
			pos := idx + 1 // 1: baud, 2: alias, 3: mode, 4: eol

			switch pos {
			case 1:
				if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
				} else if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else {
					alias = p
				}
			case 2:
				if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
				} else if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else {
					alias = p
				}
			case 3:
				if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
				} else if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else {
					alias = p
				}
			case 4:
				if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
				} else if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else {
					alias = p
				}
			default:
				if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
				}
			}
		}

		if !seen[port] {
			seen[port] = true
			results = append(results, SerialConfig{
				Port:     port,
				BaudRate: baud,
				Alias:    alias,
				HexMode:  isHex,
				EOL:      eol,
			})
		}
	}
	return results
}

// Dedicated port pipeline reading raw bytes, handling auto-reconnect, and emitting framed LogMessages
func startPortPipeline(portName string, alias string, baudRate int, portHexMode bool, colorCode string, logChan chan<- LogMessage, activePorts *sync.Map) {
	mode := &serial.Mode{
		BaudRate: baudRate,
	}

	firstAttempt := true
	for {
		port, err := serial.Open(portName, mode)
		if err != nil {
			if firstAttempt {
				logChan <- LogMessage{
					PortName:  alias,
					Direction: "SYS",
					ColorCode: "\033[1;31m",
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("❌ 打开物理串口 %s 失败: %v (已启动后台自动重连监测...)", portName, err),
				}
				firstAttempt = false
			}
			time.Sleep(2 * time.Second)
			continue
		}

		activePorts.Store(alias, port)

		if alias == portName {
			logChan <- LogMessage{
				PortName:  alias,
				Direction: "SYS",
				ColorCode: "\033[1;32m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("✅ 串口连接成功 (%d baud)", baudRate),
			}
		} else {
			logChan <- LogMessage{
				PortName:  alias,
				Direction: "SYS",
				ColorCode: "\033[1;32m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("✅ 串口连接成功 (物理端口 %s, %d baud)", portName, baudRate),
			}
		}

		buf := make([]byte, 4096)
		var lineBuf bytes.Buffer

		// --- Hex Mode Timer-based Flush ---
		hexRxChan := make(chan []byte, 100)
		hexDoneChan := make(chan struct{})

		if portHexMode {
			go func() {
				defer close(hexDoneChan)
				var hexBuf []byte
				timer := time.NewTimer(30 * time.Millisecond)
				if !timer.Stop() {
					<-timer.C
				}
				for {
					select {
					case chunk, ok := <-hexRxChan:
						if !ok {
							return // Port closed, exit goroutine
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

		// Read loop
		for {
			n, readErr := port.Read(buf)
			if readErr != nil {
				logChan <- LogMessage{
					PortName:  alias,
					Direction: "SYS",
					ColorCode: "\033[1;33m",
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("⚠️ 串口断开: %v (等待热插拔重连...)", readErr),
				}
				break
			}
			if n > 0 {
				now := time.Now()
				chunk := buf[:n]

				if portHexMode {
					hexRxChan <- append([]byte(nil), chunk...)
				} else {
					lineBuf.Write(chunk)

					for {
						lineBytes, e := lineBuf.ReadBytes('\n')
						if e != nil {
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

		if portHexMode {
			close(hexRxChan)
			<-hexDoneChan
		}

		port.Close()
		activePorts.Delete(alias)

		firstAttempt = false
		time.Sleep(2 * time.Second) // Pause 2 seconds before attempting auto-reconnect
	}
}

// Read stdin byte-by-byte in RAW terminal mode for instant non-canonical keypress & hotkey handling
func startStdinCommandReader(activePorts *sync.Map, logChan chan<- LogMessage) {
	buf := make([]byte, 1)
	// Keep the editable line separately from the cursor position.  A bytes.Buffer
	// is sufficient for appending, but it cannot represent an insertion point,
	// which is why the old implementation could only edit at the end of a line.
	var input []rune
	cursor := 0
	var history []string
	historyIdx := -1

	moveCursorLeft := func(n int) {
		if n > 0 {
			fmt.Print(strings.Repeat("\033[D", n))
		}
	}

	refreshLine := func() {
		fmt.Print("\r\033[2K")
		fmt.Print(string(input))
		moveCursorLeft(len(input) - cursor)
	}

	setInput := func(text string) {
		// Clear the old line, draw the recalled line, and leave the cursor at
		// its end. This also handles recalling a shorter history entry.
		input = []rune(text)
		cursor = len(input)
		refreshLine()
	}

	readLineBytes := func(timeout time.Duration) (byte, bool) {
		ch := make(chan byte, 1)
		go func() {
			var b [1]byte
			if n, err := os.Stdin.Read(b[:]); err == nil && n > 0 {
				ch <- b[0]
			}
		}()
		select {
		case b := <-ch:
			return b, true
		case <-time.After(timeout):
			return 0, false
		}
	}

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if n > 0 {
			b := buf[0]

			// Instant Quit Hotkey: Ctrl+] (0x1D)
			if b == 0x1D {
				if origTerminalState != nil {
					_ = term.Restore(int(os.Stdin.Fd()), origTerminalState)
				}
				logChan <- LogMessage{
					PortName:  "SYS",
					Direction: "SYS",
					ColorCode: "\033[1;33m",
					Timestamp: time.Now(),
					Content:   "👋 捕获到 Ctrl+] 退出快捷键，正在退出 multi_uart_logger...",
				}
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}

			// Handle ESC (0x1B) vs ANSI Escape Sequences (Arrow Keys, Delete, Home, End)
			if b == 0x1B {
				b2, ok2 := readLineBytes(10 * time.Millisecond)
				if !ok2 {
					// Standalone ESC: do not exit, simply ignore to prevent accidental exits
					continue
				}

				if b2 == '[' || b2 == 'O' {
					b3, ok3 := readLineBytes(10 * time.Millisecond)
					if ok3 {
						if b3 == 'A' { // Up Arrow -> Recall Previous Command
							if len(history) > 0 {
								if historyIdx == -1 {
									historyIdx = len(history) - 1
								} else if historyIdx > 0 {
									historyIdx--
								}
								setInput(history[historyIdx])
							}
							continue
						} else if b3 == 'B' { // Down Arrow -> Recall Next Command
							if historyIdx >= 0 {
								if historyIdx < len(history)-1 {
									historyIdx++
									setInput(history[historyIdx])
								} else {
									historyIdx = -1
									setInput("")
								}
							}
							continue
						} else if b3 == 'D' { // Left Arrow
							if cursor > 0 {
								cursor--
								moveCursorLeft(1)
							}
							continue
						} else if b3 == 'C' { // Right Arrow
							if cursor < len(input) {
								fmt.Print("\033[C")
								cursor++
							}
							continue
						} else if b3 == 'H' { // Home Key (\x1b[H or \x1bOH)
							cursor = 0
							refreshLine()
							continue
						} else if b3 == 'F' { // End Key (\x1b[F or \x1bOF)
							cursor = len(input)
							refreshLine()
							continue
						} else if b3 >= '0' && b3 <= '9' {
							b4, ok4 := readLineBytes(10 * time.Millisecond)
							if ok4 && b4 == '~' {
								if b3 == '3' { // Delete Key (\x1b[3~)
									if cursor < len(input) {
										input = append(input[:cursor], input[cursor+1:]...)
										refreshLine()
									}
								} else if b3 == '1' { // Home Key (\x1b[1~)
									cursor = 0
									refreshLine()
								} else if b3 == '4' { // End Key (\x1b[4~)
									cursor = len(input)
									refreshLine()
								}
							}
							continue
						}
					}
				}
				continue
			}

			// Handle Backspace (0x08 or 0x7F)
			if b == 0x08 || b == 0x7F {
				if cursor > 0 {
					input = append(input[:cursor-1], input[cursor:]...)
					cursor--
					refreshLine()
				}
				continue
			}

			// Handle Enter (\r or \n)
			if b == '\r' || b == '\n' {
				text := strings.TrimSpace(string(input))
				input = nil
				cursor = 0
				fmt.Print("\r\n")
				if text != "" {
					if len(history) == 0 || history[len(history)-1] != text {
						history = append(history, text)
					}
					historyIdx = -1

					processInputCmd(text, activePorts, logChan)
				}
				continue
			}

			// Instant passthrough for Control Characters:
			// Ctrl+C (0x03), Ctrl+Z (0x1A), Ctrl+D (0x04), Ctrl+\ (0x1C), etc.
			if b < 0x20 && b != '\t' {
				ctrlByte := []byte{b}
				count := 0
				activePorts.Range(func(key, value any) bool {
					if p, ok := value.(serial.Port); ok {
						_, _ = p.Write(ctrlByte)
						count++
					}
					return true
				})
				logChan <- LogMessage{
					PortName:  "SYS",
					Direction: "SYS",
					ColorCode: "\033[1;35m",
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("📢 [键盘物理按键 0x%02X] 已向 %d 个串口透传广播下发", b, count),
				}
				continue
			}

			// Regular printable character: buffer it and echo locally
			if b >= 0x20 {
				if cursor == len(input) {
					input = append(input, rune(b))
					cursor++
					fmt.Printf("%c", b)
				} else {
					input = append(input, 0)
					copy(input[cursor+1:], input[cursor:])
					input[cursor] = rune(b)
					cursor++
					refreshLine()
				}
			}
		}
	}
}

// Helper to parse text shorthands like "ctrl-c", "ctrl-z", "ctrl-d", "ctrl-\" into raw ASCII control bytes
func parseCtrlCommand(cmdStr string) ([]byte, bool) {
	s := strings.ToLower(strings.TrimSpace(cmdStr))
	if strings.HasPrefix(s, "ctrl-") || strings.HasPrefix(s, "ctrl+") {
		sub := s[5:]
		if len(sub) == 1 {
			ch := sub[0]
			if (ch >= 'a' && ch <= 'z') || ch == '\\' || ch == '[' || ch == ']' || ch == '^' || ch == '_' {
				return []byte{ch & 0x1F}, true
			}
		}
	}
	return nil, false
}

func processInputCmd(text string, activePorts *sync.Map, logChan chan<- LogMessage) {
	targetName, targetPort, cmdStr, isTargeted := parseTargetAndCommand(text, activePorts)

	var cmdBytes []byte
	var err error

	if ctrlBytes, ok := parseCtrlCommand(cmdStr); ok {
		cmdBytes = ctrlBytes
	}

	if isTargeted {
		if cmdBytes == nil {
			if isPortHexMode(targetName) {
				// Remove spaces and parse hex
				cleanHex := strings.ReplaceAll(cmdStr, " ", "")
				cmdBytes, err = hex.DecodeString(cleanHex)
				if err != nil {
					logChan <- LogMessage{
						PortName:  "SYS",
						Direction: "SYS",
						ColorCode: "\033[1;31m", // Red
						Timestamp: time.Now(),
						Content:   fmt.Sprintf("❌ Hex 格式错误，忽略发送 -> %s: %s", targetName, cmdStr),
					}
					return
				}
			} else {
				eol := getPortEOL(targetName)
				cmdBytes = []byte(cmdStr + eol)
			}
		}

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
		var hexBytes []byte
		cleanHex := strings.ReplaceAll(cmdStr, " ", "")
		if b, e := hex.DecodeString(cleanHex); e == nil && len(b) > 0 {
			hexBytes = b
		}

		activePorts.Range(func(key, value any) bool {
			if p, ok := value.(serial.Port); ok {
				portName := key.(string)
				var sendData []byte
				if cmdBytes != nil {
					sendData = cmdBytes
				} else if isPortHexMode(portName) {
					if hexBytes != nil {
						sendData = hexBytes
					} else {
						// Content is not valid hex, skip sending to hex-only port
						return true
					}
				} else {
					eol := getPortEOL(portName)
					sendData = []byte(cmdStr + eol)
				}

				if len(sendData) > 0 {
					_, _ = p.Write(sendData)
					count++
					logChan <- LogMessage{
						PortName:  portName,
						Direction: "TX",
						ColorCode: "\033[1;35m", // Purple for TX
						Timestamp: time.Now(),
						Content:   cmdStr,
					}
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
		"  3. 特殊控制信号指令 (支持 ctrl-a 到 ctrl-z, ctrl-\\ 等):",
		"     - 示例: ctrl-c     (下发 0x03 ETX / 打断信号)",
		"     - 示例: ctrl-z     (下发 0x1A SUB / 挂起信号)",
		"     - 示例: ctrl-d     (下发 0x04 EOT / EOF 退出 Shell)",
		"     - 示例: A1: ctrl-c (定向向别名为 A1 的串口下发 0x03)",
		"  4. Logger 程序退出指令:",
		"     - 按 Ctrl+] 退出 multi_uart_logger",
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

func extractPortNum(s string) int {
	sUpper := strings.ToUpper(strings.TrimSpace(s))
	if strings.HasPrefix(sUpper, "COM") {
		if n, err := strconv.Atoi(sUpper[3:]); err == nil {
			return n
		}
	}
	// Extract trailing digits if any (e.g. /dev/ttyUSB0)
	idx := len(s)
	for idx > 0 && s[idx-1] >= '0' && s[idx-1] <= '9' {
		idx--
	}
	if idx < len(s) {
		if n, err := strconv.Atoi(s[idx:]); err == nil {
			return n
		}
	}
	return 999999
}

func listAvailablePorts() {
	ports, err := serial.GetPortsList()
	if err != nil || len(ports) == 0 {
		fmt.Println("ℹ️ 未找到任何可用的串口!")
		return
	}

	sort.Slice(ports, func(i, j int) bool {
		n1 := extractPortNum(ports[i])
		n2 := extractPortNum(ports[j])
		if n1 != n2 {
			return n1 < n2
		}
		return ports[i] < ports[j]
	})

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
