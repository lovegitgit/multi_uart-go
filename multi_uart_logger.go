package main

import (
	"bufio"
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
	HciMode  bool
	EOL      string
}

var (
	telnetClientsMutex sync.Mutex
	telnetClients      = make(map[net.Conn]bool)
	outMutex           sync.Mutex
	hexMode            bool
	portHexModes       sync.Map // lower(port/alias) -> bool
	hciMode            bool
	portHciModes       sync.Map // lower(port/alias) -> bool
	globalEOL          = "\r\n"
	portEOLModes       sync.Map // lower(port/alias) -> string
	plainMode          bool
	noTime             bool
	charMode           bool
	logFileWriter      *os.File

	origTerminalState *term.State
)

func isPortHexMode(name string) bool {
	if v, ok := portHexModes.Load(strings.ToLower(name)); ok {
		return v.(bool)
	}
	return hexMode
}

func isPortHciMode(name string) bool {
	if v, ok := portHciModes.Load(strings.ToLower(name)); ok {
		return v.(bool)
	}
	return hciMode
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

	flag.Var(&portFlags, "p", "串口配置 (单字母用 -p, 多字母可用 --port) 格式: COMx[,Baud[,Alias[,Mode[,EOL]]]] (如: -p COM25,115200,A1,text,lf 或占位覆盖: -p COM3,-,-,text 或引号留空: -p \"COM5,,,,cr\")")
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
	flag.BoolVar(&hciMode, "hci", false, "启用 BLE HCI 指令解析与快捷预设指令模式 (支持 /cmds, /reset, /adv 等及 Tab 补全)")
	flag.StringVar(&eolFlag, "eol", "crlf", "全局文本模式发送行尾换行符: crlf (默认), lf, cr, none")
	flag.BoolVar(&plainMode, "plain", false, "纯净终端直通模式 (隐藏端口名、时间戳及方向箭头，如 PuTTY/minicom)")
	flag.BoolVar(&plainMode, "no-prefix", false, "同 --plain")
	flag.BoolVar(&noTime, "no-time", false, "隐藏时间戳 (保留端口名及方向箭头)")
	flag.BoolVar(&charMode, "char", false, "单键实时透传模式 (按键即刻发送无需回车，支持 MobaXterm 单键菜单交互)")
	flag.BoolVar(&charMode, "raw-input", false, "同 --char")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, " 🚀 高性能多串口实时日志汇总监控工具 (Multi-UART Logger)              \n")
		fmt.Fprintf(os.Stderr, "=======================================================================\n")
		fmt.Fprintf(os.Stderr, "用法:\n")
		fmt.Fprintf(os.Stderr, "  %s --list\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM23,115200 -p COM24,115200\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM3 COM4 COM5 --hex -p COM3,-,-,text\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM5,-,-,-,cr -p COM6,115200,-,text,lf --eol crlf\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM24 --plain\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -p COM24,115200,BLE,hci\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s --port COM23,115200 --listen 0.0.0.0:8023 --user admin --pass 123456 --hex\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "参数说明:\n")
		fmt.Fprintf(os.Stderr, "  -p, --port string\n\t串口配置，格式: COMx[,Baud[,Alias[,Mode[,EOL]]]] (如: -p COM25,115200,A1,text,lf 或 -p COM24,115200,BLE,hci 或占位覆盖: -p COM3,-,-,text 或引号留空: -p \"COM5,,,,cr\")\n")
		fmt.Fprintf(os.Stderr, "  --hci\n\t启用全局 BLE HCI 指令解析模式 (包含 /cmds 快捷指令库与 Tab 自动补全)\n")
		fmt.Fprintf(os.Stderr, "  --plain\n\t纯净终端直通模式 (隐藏端口名、时间戳及方向箭头，支持 \\r 原地刷新，如 MobaXterm 原生终端)\n")
		fmt.Fprintf(os.Stderr, "  --char\n\t单键实时透传模式 (按键即刻发送无需回车，支持 MobaXterm 单键菜单交互)\n")
		fmt.Fprintf(os.Stderr, "  --no-time\n\t隐藏时间戳 (保留端口名及方向箭头)\n")
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
		} else if args[i] == "--plain" || args[i] == "-plain" || args[i] == "--no-prefix" || args[i] == "-no-prefix" {
			plainMode = true
		} else if args[i] == "--char" || args[i] == "-char" || args[i] == "--raw-input" || args[i] == "-raw-input" {
			charMode = true
		} else if args[i] == "--no-time" || args[i] == "-no-time" {
			noTime = true
		} else if args[i] == "--hex" || args[i] == "-hex" {
			hexMode = true
		} else if args[i] == "--hci" || args[i] == "-hci" {
			hciMode = true
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

	configs := parseSerialConfigs(rawPortConfigs, defaultBaud, hexMode, hciMode, globalEOL)

	for _, cfg := range configs {
		portHexModes.Store(strings.ToLower(cfg.Port), cfg.HexMode)
		portHexModes.Store(strings.ToLower(cfg.Alias), cfg.HexMode)
		portHciModes.Store(strings.ToLower(cfg.Port), cfg.HciMode)
		portHciModes.Store(strings.ToLower(cfg.Alias), cfg.HciMode)
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
		if cfg.HciMode {
			modeTag = "\033[1;35m[HCI]\033[0m"
		} else if cfg.HexMode {
			modeTag = "\033[1;33m[HEX]\033[0m"
		}
		fmt.Printf("   [%d] %s%s%s%s | 波特率: %d | 模式: %s | EOL: %s\n", i+1, color, cfg.Port, colorReset, aliasStr, cfg.BaudRate, modeTag, formatEOLDesc(cfg.EOL))
	}
	if plainMode {
		fmt.Printf(" 💡 [显示模式] 纯净直通终端模式 (--plain) [无时间戳/前缀，支持 \\r 原地自动刷新，对齐 MobaXterm]\n")
	} else if noTime {
		fmt.Printf(" 💡 [显示模式] 精简模式 (--no-time) [隐藏时间戳]\n")
	} else {
		fmt.Printf(" 💡 [时间戳格式] %s\n", getTimeFormatDesc(showFullDate, timeOnly))
	}
	fmt.Printf(" 💡 [发送换行符] 全局默认: %s (可用 --eol 设置全局, 或 -p COMx,,,,<eol> 单独覆盖)\n", formatEOLDesc(globalEOL))
	if charMode {
		fmt.Printf(" 💡 [输入模式] 单键实时透传模式 (--char) [按键即发无需回车，对齐 MobaXterm 单键菜单交互]\n")
	} else {
		fmt.Printf(" 💡 [输入模式] 行缓冲编辑模式 (默认) [按回车发送; 定向: COMx: cmd; 广播: cmd; 可加 --char 开启单键透传]\n")
	}
	anyHci := hciMode
	for _, cfg := range configs {
		if cfg.HciMode {
			anyHci = true
			break
		}
	}
	if anyHci {
		fmt.Printf(" 💡 [HCI 模式] 已启用 BLE HCI 指令解析; 输入 /cmds 查看快捷指令, 支持 Tab 自动补全\n")
	}
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

	var termFormat, fileFormat string
	if noTime {
		termFormat = fmt.Sprintf("%%s[%%-%ds]%%s %%s%%s", maxNameLen)
		fileFormat = fmt.Sprintf("[%%-%ds] %%s%%s\n", maxNameLen)
	} else {
		termFormat = fmt.Sprintf("%%s[%%-%ds]%%s[%%s] %%s%%s", maxNameLen)
		fileFormat = fmt.Sprintf("[%%-%ds][%%s] %%s%%s\n", maxNameLen)
	}

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
			if count > 1 {
				logChan <- LogMessage{
					PortName:  "SYS",
					Direction: "SYS",
					ColorCode: "\033[1;35m",
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("📢 [捕获系统信号 %s] 已向 %d 个串口透传广播下发 0x%02X", sigName, count, ctrlBytes[0]),
				}
			}
		}
	}()

	// 2.5 Start Telnet Server if requested
	if listenAddr != "" {
		go startTelnetServer(listenAddr, telnetUser, telnetPass, &activePorts, logChan)
	}

	// 3. Main Thread: Collect & Merge Output Loop
	for msg := range logChan {
		var timeStr string
		if !noTime {
			if timeOnly {
				timeStr = msg.Timestamp.Format("15:04:05.000000")
			} else if showFullDate {
				timeStr = msg.Timestamp.Format("2006-01-02 15:04:05.000000")
			} else {
				timeStr = msg.Timestamp.Format("01-02 15:04:05.000000") // Restored microsecond precision
			}
		}

		// Ensure content never contains orphan \r that could reset cursor to column 0
		safeContent := strings.ReplaceAll(msg.Content, "\r", "")

		var dirStrTerm, dirStrFile string
		if msg.Direction == "RX" {
			if strings.HasPrefix(safeContent, "💡") || strings.HasPrefix(safeContent, "   ") {
				dirStrTerm = "   "
				dirStrFile = "   "
			} else {
				dirStrTerm = "\033[1;36m<<\033[0m "
				dirStrFile = "<< "
			}
		} else if msg.Direction == "TX" {
			if strings.HasPrefix(safeContent, "💡") || strings.HasPrefix(safeContent, "   ") {
				dirStrTerm = "   "
				dirStrFile = "   "
			} else {
				dirStrTerm = "\033[1;35m>>\033[0m " // Magenta for TX
				dirStrFile = ">> "
			}
		} else {
			dirStrTerm = ""
			dirStrFile = ""
		}

		var termLine, plainLine string
		if plainMode {
			if msg.Direction == "SYS" {
				termLine = fmt.Sprintf("%s%s%s", msg.ColorCode, safeContent, colorReset)
				plainLine = fmt.Sprintf("[SYS] %s\n", safeContent)
			} else if msg.Direction == "TX" {
				if isPortHciMode(msg.PortName) || strings.HasPrefix(safeContent, "💡") {
					if strings.HasPrefix(safeContent, "💡") || strings.HasPrefix(safeContent, "   ") {
						termLine = fmt.Sprintf("%s   %s%s", msg.ColorCode, safeContent, colorReset)
						plainLine = fmt.Sprintf("   %s\n", safeContent)
					} else {
						termLine = fmt.Sprintf("%s>> %s%s", msg.ColorCode, safeContent, colorReset)
						plainLine = fmt.Sprintf(">> %s\n", safeContent)
					}
				} else {
					plainLine = fmt.Sprintf(">> %s\n", safeContent)
				}
			} else {
				if strings.HasPrefix(safeContent, "💡") || strings.HasPrefix(safeContent, "   ") {
					termLine = fmt.Sprintf("%s   %s%s", msg.ColorCode, safeContent, colorReset)
					plainLine = fmt.Sprintf("   %s\n", safeContent)
				} else {
					termLine = safeContent
					plainLine = safeContent + "\n"
				}
			}
		} else {
			if noTime {
				termLine = fmt.Sprintf(termFormat, msg.ColorCode, msg.PortName, colorReset, dirStrTerm, safeContent)
				plainLine = fmt.Sprintf(fileFormat, msg.PortName, dirStrFile, safeContent)
			} else {
				termLine = fmt.Sprintf(termFormat, msg.ColorCode, msg.PortName, colorReset, timeStr, dirStrTerm, safeContent)
				plainLine = fmt.Sprintf(fileFormat, msg.PortName, timeStr, dirStrFile, safeContent)
			}
		}

		outMutex.Lock()
		if termLine != "" || (plainMode && msg.Direction == "RX") {
			fmt.Print(termLine + "\r\n")
		}

		// File Output (Plain text without ANSI color codes)
		if logFileWriter != nil && plainLine != "" {
			_, _ = logFileWriter.WriteString(plainLine)
		}

		// Telnet Output
		if termLine != "" {
			telnetClientsMutex.Lock()
			for conn := range telnetClients {
				_, err := conn.Write([]byte(termLine + "\r\n"))
				if err != nil {
					conn.Close()
					delete(telnetClients, conn)
				}
			}
			telnetClientsMutex.Unlock()
		}

		outMutex.Unlock()
	}
}



// Parse input arguments like ["COM23,115200", "COM24,115200", "COM25,921600,A1,hex", "COM5,,,,cr"]
func parseSerialConfigs(args []string, defaultBaud int, globalHex bool, globalHci bool, defaultEOL string) []SerialConfig {
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
		isHex := globalHex || globalHci
		isHci := globalHci
		eol := defaultEOL

		for idx, part := range parts[1:] {
			p := strings.TrimSpace(part)
			if p == "" || p == "-" || p == "_" {
				continue // empty slot or placeholder (e.g. com3,-,-,text): preserve default/inherited value
			}
			pLower := strings.ToLower(p)
			pos := idx + 1 // 1: baud, 2: alias, 3: mode, 4: eol

			switch pos {
			case 1:
				if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else if pLower == "hci" {
					isHci = true
					isHex = true
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
					isHci = false
				} else if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else {
					alias = p
				}
			case 2:
				if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else if pLower == "hci" {
					isHci = true
					isHex = true
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
					isHci = false
				} else if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else {
					alias = p
				}
			case 3:
				if pLower == "hci" {
					isHci = true
					isHex = true
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
					isHci = false
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
				} else if pLower == "hci" {
					isHci = true
					isHex = true
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
					isHci = false
				} else if b, err := strconv.Atoi(p); err == nil && b > 0 {
					baud = b
				} else {
					alias = p
				}
			default:
				if parsedEOL, ok := parseEOL(pLower); ok {
					eol = parsedEOL
				} else if pLower == "hci" {
					isHci = true
					isHex = true
				} else if pLower == "hex" || pLower == "raw" {
					isHex = true
				} else if pLower == "text" || pLower == "ascii" || pLower == "str" {
					isHex = false
					isHci = false
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
				HciMode:  isHci,
				EOL:      eol,
			})
		}
	}
	return results
}

// extractLines splits data by line breaks (\r\n, \n\r, \r, \n).
// If flushAll is false and the buffer ends mid-line or at an unconfirmed trailing \r/\n,
// the unconsumed remainder is returned.
func extractLines(data []byte, flushAll bool) ([]string, []byte) {
	var lines []string
	start := 0
	n := len(data)
	i := 0

	for i < n {
		b := data[i]
		if b == '\r' || b == '\n' {
			// If we're at the very last byte and not flushing,
			// wait in case a matching \n or \r arrives in the next packet.
			if !flushAll && i == n-1 {
				break
			}

			lineStr := string(data[start:i])
			// Consume pair if \r\n or \n\r
			if i+1 < n && ((b == '\r' && data[i+1] == '\n') || (b == '\n' && data[i+1] == '\r')) {
				i += 2
			} else {
				i += 1
			}
			start = i

			lineStr = strings.Trim(lineStr, "\r\n")
			if strings.Contains(lineStr, "\r") {
				parts := strings.Split(lineStr, "\r")
				for _, p := range parts {
					p = strings.Trim(p, "\r\n")
					lines = append(lines, p)
				}
			} else {
				lines = append(lines, lineStr)
			}
		} else {
			i++
		}
	}

	if start < n {
		if flushAll {
			remStr := strings.Trim(string(data[start:]), "\r\n")
			if strings.Contains(remStr, "\r") {
				parts := strings.Split(remStr, "\r")
				for _, p := range parts {
					p = strings.Trim(p, "\r\n")
					lines = append(lines, p)
				}
			} else {
				lines = append(lines, remStr)
			}
			return lines, nil
		}
		return lines, append([]byte(nil), data[start:]...)
	}

	return lines, nil
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

				flushPacket := func(pkt []byte) {
					if len(pkt) == 0 {
						return
					}
					var sb strings.Builder
					for j, b := range pkt {
						if j > 0 {
							sb.WriteByte(' ')
						}
						fmt.Fprintf(&sb, "%02X", b)
					}
					now := time.Now()
					logChan <- LogMessage{
						PortName:  alias,
						Direction: "RX",
						ColorCode: colorCode,
						Timestamp: now,
						Content:   sb.String(),
					}
					if isPortHciMode(portName) {
						if explainLines := parseHciEventLines(pkt); len(explainLines) > 0 {
							for _, l := range explainLines {
								logChan <- LogMessage{
									PortName:  alias,
									Direction: "RX",
									ColorCode: "\033[1;36m",
									Timestamp: now,
									Content:   l,
								}
							}
						}
					}
				}

				for {
					select {
					case chunk, ok := <-hexRxChan:
						if !ok {
							flushPacket(hexBuf)
							return // Port closed, exit goroutine
						}
						hexBuf = append(hexBuf, chunk...)

						// In HCI mode: frame complete H4 packets immediately
						if isPortHciMode(portName) {
							for len(hexBuf) >= 3 {
								pktType := hexBuf[0]
								expectedLen := 0
								if pktType == 0x04 { // HCI Event
									paramLen := int(hexBuf[2])
									expectedLen = 3 + paramLen
								} else if pktType == 0x01 { // HCI Command
									if len(hexBuf) < 4 {
										break
									}
									paramLen := int(hexBuf[3])
									expectedLen = 4 + paramLen
								} else if pktType == 0x02 { // HCI ACL Data
									if len(hexBuf) < 5 {
										break
									}
									dataLen := int(hexBuf[3]) | (int(hexBuf[4]) << 8)
									expectedLen = 5 + dataLen
								} else {
									break
								}

								if expectedLen > 0 && len(hexBuf) >= expectedLen {
									packet := hexBuf[:expectedLen]
									hexBuf = hexBuf[expectedLen:]
									flushPacket(packet)
									continue
								}
								break
							}
						}

						if len(hexBuf) > 0 {
							timer.Reset(30 * time.Millisecond) // Flush after 30ms of silence
						} else {
							if !timer.Stop() {
								select {
								case <-timer.C:
								default:
								}
							}
						}
					case <-timer.C:
						if len(hexBuf) > 0 {
							flushPacket(hexBuf)
							hexBuf = hexBuf[:0]
						}
					}
				}
			}()
		}

		// --- Text Mode Timer-based Flush & Line Framing ---
		textRxChan := make(chan []byte, 200)
		textDoneChan := make(chan struct{})

		if !portHexMode && !plainMode {
			go func() {
				defer close(textDoneChan)
				var rawBuf []byte
				timer := time.NewTimer(40 * time.Millisecond)
				if !timer.Stop() {
					<-timer.C
				}

				flushLines := func(flushAll bool) {
					if len(rawBuf) == 0 {
						return
					}
					lines, remainder := extractLines(rawBuf, flushAll)
					rawBuf = remainder
					now := time.Now()
					for _, line := range lines {
						if strings.TrimSpace(line) != "" {
							logChan <- LogMessage{
								PortName:  alias,
								Direction: "RX",
								ColorCode: colorCode,
								Timestamp: now,
								Content:   line,
							}
						}
					}
				}

				for {
					select {
					case chunk, ok := <-textRxChan:
						if !ok {
							flushLines(true)
							return
						}
						rawBuf = append(rawBuf, chunk...)
						flushLines(false)
						if len(rawBuf) > 0 {
							timer.Reset(40 * time.Millisecond)
						} else {
							if !timer.Stop() {
								select {
								case <-timer.C:
								default:
								}
							}
						}
					case <-timer.C:
						flushLines(true)
					}
				}
			}()
		}

		lastByteWasCR := false

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
				chunk := buf[:n]

				if portHexMode {
					hexRxChan <- append([]byte(nil), chunk...)
				} else if plainMode {
					// MobaXterm-aligned Raw Stream Passthrough:
					// Direct streaming allows \r to refresh the line in place without spurious newlines
					var outBuf []byte
					for i := 0; i < len(chunk); i++ {
						b := chunk[i]
						if b == '\n' {
							if (i > 0 && chunk[i-1] == '\r') || (i == 0 && lastByteWasCR) {
								outBuf = append(outBuf, '\n')
							} else {
								outBuf = append(outBuf, '\r', '\n')
							}
						} else {
							outBuf = append(outBuf, b)
						}
					}
					lastByteWasCR = (chunk[len(chunk)-1] == '\r')

					outMutex.Lock()
					_, _ = os.Stdout.Write(outBuf)
					outMutex.Unlock()

					// File Output
					if logFileWriter != nil {
						_, _ = logFileWriter.Write(outBuf)
					}

					// Telnet Output
					telnetClientsMutex.Lock()
					for conn := range telnetClients {
						_, err := conn.Write(outBuf)
						if err != nil {
							conn.Close()
							delete(telnetClients, conn)
						}
					}
					telnetClientsMutex.Unlock()
				} else {
					textRxChan <- append([]byte(nil), chunk...)
				}
			}
		}

		if portHexMode {
			close(hexRxChan)
			<-hexDoneChan
		} else if !plainMode {
			close(textRxChan)
			<-textDoneChan
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

	handleTabCompletion := func() {
		currStr := string(input[:cursor])
		// 1. If empty or single slash: show list of all preset commands
		if currStr == "" || currStr == "/" {
			outMutex.Lock()
			fmt.Printf("\r\n\033[1;36m💡 预设 HCI 快捷指令: %s\033[0m\r\n", strings.Join(presetHciCmdOrder, "  "))
			outMutex.Unlock()
			if currStr == "" {
				input = []rune("/")
				cursor = 1
			}
			refreshLine()
			return
		}

		// 2. Command name or parameter completion
		if strings.HasPrefix(currStr, "/") {
			if strings.Contains(currStr, " ") {
				// Parameter completion
				parts := strings.Fields(currStr)
				cmdVerb := strings.ToLower(parts[0])
				switch cmdVerb {
				case "/adv":
					if strings.HasSuffix(currStr, "on") {
						setInput("/adv off")
					} else {
						setInput("/adv on")
					}
				case "/scan":
					if strings.HasSuffix(currStr, "on") {
						setInput("/scan off")
					} else {
						setInput("/scan on")
					}
				case "/advparam":
					if len(parts) == 1 {
						setInput("/advparam 100")
					}
				case "/scanparam":
					if len(parts) == 1 {
						setInput("/scanparam active 100 50")
					}
				case "/dtm-tx":
					if len(parts) == 1 {
						setInput("/dtm-tx 0 37 0")
					}
				case "/dtm-rx":
					if len(parts) == 1 {
						setInput("/dtm-rx 0")
					}
				case "/txpower", "/nxp-txpower":
					if len(parts) == 1 {
						setInput("/txpower 8 1")
					} else if len(parts) == 2 {
						setInput(parts[0] + " " + parts[1] + " 1")
					}
				default:
					if hint := getHciCmdParamHint(cmdVerb); hint != "" {
						outMutex.Lock()
						fmt.Printf("\r\n\033[1;33m💡 参数提示: %s\033[0m\r\n", hint)
						outMutex.Unlock()
						refreshLine()
					}
				}
				return
			}

			// Complete command name
			var matches []string
			lowerCurr := strings.ToLower(currStr)
			for _, name := range presetHciCmdOrder {
				if strings.HasPrefix(name, lowerCurr) {
					matches = append(matches, name)
				}
			}

			if len(matches) == 1 {
				match := matches[0]
				newPrefix := match + " "
				input = append([]rune(newPrefix), input[cursor:]...)
				cursor = len(newPrefix)
				refreshLine()
			} else if len(matches) > 1 {
				lcp := findLCP(matches)
				if len(lcp) > len(currStr) {
					input = append([]rune(lcp), input[cursor:]...)
					cursor = len(lcp)
				}
				outMutex.Lock()
				fmt.Printf("\r\n\033[1;36m💡 候选指令: %s\033[0m\r\n", strings.Join(matches, "  "))
				outMutex.Unlock()
				refreshLine()
			}
			return
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

			// Single-Key Real-Time Passthrough (Char Mode / MobaXterm style)
			if charMode {
				// Enter key (\r or \n): send configured EOL to active text ports
				if b == '\r' || b == '\n' {
					activePorts.Range(func(key, value any) bool {
						portName, okKey := key.(string)
						p, okVal := value.(serial.Port)
						if okKey && okVal && !isPortHexMode(portName) {
							eol := getPortEOL(portName)
							if len(eol) > 0 {
								_, _ = p.Write([]byte(eol))
							} else {
								_, _ = p.Write([]byte{b})
							}
						}
						return true
					})
					continue
				}

				// Handle ESC & ANSI Escape Sequences (e.g. Arrow keys, Home, End)
				if b == 0x1B {
					seq := []byte{0x1B}
					for {
						bNext, okNext := readLineBytes(15 * time.Millisecond)
						if !okNext {
							break
						}
						seq = append(seq, bNext)
						if (bNext >= 'A' && bNext <= 'Z') || (bNext >= 'a' && bNext <= 'z') || bNext == '~' {
							break
						}
					}
					activePorts.Range(func(key, value any) bool {
						portName, okKey := key.(string)
						p, okVal := value.(serial.Port)
						if okKey && okVal && !isPortHexMode(portName) {
							_, _ = p.Write(seq)
						}
						return true
					})
					continue
				}

				// All other characters (single keypress like 't', 'a', '1', Backspace, Tab, Ctrl+C, etc.):
				// Send directly to active text-mode ports
				activePorts.Range(func(key, value any) bool {
					portName, okKey := key.(string)
					p, okVal := value.(serial.Port)
					if okKey && okVal && !isPortHexMode(portName) {
						_, _ = p.Write([]byte{b})
					}
					return true
				})
				continue
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
				} else {
					// Empty Enter: wake up text-mode serial terminals by sending configured EOL,
					// while strictly skipping hex-mode ports to avoid sending invalid bytes.
					processEmptyEnter(activePorts)
				}
				continue
			}

			// Handle Tab (0x09): Auto-completion & Suggestions
			if b == '\t' || b == 0x09 {
				handleTabCompletion()
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
				if count > 1 {
					logChan <- LogMessage{
						PortName:  "SYS",
						Direction: "SYS",
						ColorCode: "\033[1;35m",
						Timestamp: time.Now(),
						Content:   fmt.Sprintf("📢 [键盘物理按键 0x%02X] 已向 %d 个串口透传广播下发", b, count),
					}
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

// parseHexInput parses user hex input string, supporting space-separated tokens with single-digit padding (e.g. "01 C 0 0" -> 01 0c 00 00)
func parseHexInput(s string) ([]byte, error) {
	fields := strings.Fields(s)
	if len(fields) > 1 {
		var sb strings.Builder
		for _, f := range fields {
			if len(f) == 1 {
				sb.WriteByte('0')
				sb.WriteString(f)
			} else {
				sb.WriteString(f)
			}
		}
		clean := sb.String()
		if len(clean)%2 != 0 {
			return nil, fmt.Errorf("hex string has odd length (%d chars)", len(clean))
		}
		return hex.DecodeString(clean)
	}
	clean := strings.ReplaceAll(s, " ", "")
	if len(clean)%2 != 0 {
		return nil, fmt.Errorf("hex string has odd length (%d chars)", len(clean))
	}
	return hex.DecodeString(clean)
}

// processEmptyEnter sends configured EOL to active text-mode ports to wake up terminals/shells,
// while strictly skipping hex-mode ports to prevent sending invalid data.
func processEmptyEnter(activePorts *sync.Map) {
	activePorts.Range(func(key, value any) bool {
		portName, okKey := key.(string)
		p, okVal := value.(serial.Port)
		if !okKey || !okVal {
			return true
		}

		// Strictly skip hex-mode ports: do not send invalid newline to binary/hex devices
		if isPortHexMode(portName) {
			return true
		}

		// Text mode port: send port-specific or global EOL
		eol := getPortEOL(portName)
		if len(eol) > 0 {
			_, _ = p.Write([]byte(eol))
		}
		return true
	})
}

func processInputCmd(text string, activePorts *sync.Map, logChan chan<- LogMessage) {
	targetName, targetPort, cmdStr, isTargeted := parseTargetAndCommand(text, activePorts)

	// Check if this is an HCI slash command (e.g. /cmds, /reset, /adv on)
	if strings.HasPrefix(cmdStr, "/") {
		handleHciSlashCommand(targetName, targetPort, isTargeted, cmdStr, activePorts, logChan)
		return
	}

	var cmdBytes []byte
	var err error

	if ctrlBytes, ok := parseCtrlCommand(cmdStr); ok {
		cmdBytes = ctrlBytes
	}

	if isTargeted {
		if cmdBytes == nil {
			if isPortHexMode(targetName) {
				if cmdStr == "" {
					return // Skip empty enter for targeted hex port
				}
				cmdBytes, err = parseHexInput(cmdStr)
				if err != nil {
					logChan <- LogMessage{
						PortName:  "SYS",
						Direction: "SYS",
						ColorCode: "\033[1;31m", // Red
						Timestamp: time.Now(),
						Content:   fmt.Sprintf("❌ Hex 格式错误，忽略发送 -> %s: %s (%v)", targetName, cmdStr, err),
					}
					return
				}
			} else {
				eol := getPortEOL(targetName)
				cmdBytes = []byte(cmdStr + eol)
			}
		}

		if len(cmdBytes) > 0 {
			_, err := targetPort.Write(cmdBytes)
			if err != nil {
				logChan <- LogMessage{
					PortName:  "SYS",
					Direction: "SYS",
					ColorCode: "\033[1;31m",
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("❌ [发送失败 -> %s]: %v", targetName, err),
				}
			} else if cmdStr != "" {
				now := time.Now()
				logChan <- LogMessage{
					PortName:  targetName,
					Direction: "TX",
					ColorCode: "\033[1;35m", // Purple for TX
					Timestamp: now,
					Content:   cmdStr,
				}
				if isPortHciMode(targetName) && len(cmdBytes) > 0 {
					if explainLines := parseHciCommandLines(cmdBytes); len(explainLines) > 0 {
						for _, l := range explainLines {
							logChan <- LogMessage{
								PortName:  targetName,
								Direction: "TX",
								ColorCode: "\033[1;35m",
								Timestamp: now,
								Content:   l,
							}
						}
					}
				}
			}
		}
	} else {
		// Broadcast command to all active ports
		totalActive := 0
		hexPortCount := 0
		textPortCount := 0
		activePorts.Range(func(key, value any) bool {
			if _, ok := value.(serial.Port); ok {
				totalActive++
				portName := key.(string)
				if isPortHexMode(portName) {
					hexPortCount++
				} else {
					textPortCount++
				}
			}
			return true
		})

		if totalActive == 0 {
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;33m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("⚠️ 当前无可用已连接串口，命令未发送: %s", cmdStr),
			}
			return
		}

		var hexBytes []byte
		var hexErr error
		if hexPortCount > 0 && cmdBytes == nil {
			hexBytes, hexErr = parseHexInput(cmdStr)
			if hexErr != nil && textPortCount == 0 {
				// All active ports are in Hex mode, but input is not valid hex!
				logChan <- LogMessage{
					PortName:  "SYS",
					Direction: "SYS",
					ColorCode: "\033[1;31m", // Red
					Timestamp: time.Now(),
					Content:   fmt.Sprintf("❌ Hex 格式错误，未向串口发送: %s (%v)", cmdStr, hexErr),
				}
				return
			}
		}

		count := 0
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
						// Content is not valid hex, skip sending to this hex-only port
						return true
					}
				} else {
					eol := getPortEOL(portName)
					sendData = []byte(cmdStr + eol)
				}

				if len(sendData) > 0 {
					_, _ = p.Write(sendData)
					count++
					now := time.Now()
					logChan <- LogMessage{
						PortName:  portName,
						Direction: "TX",
						ColorCode: "\033[1;35m", // Purple for TX
						Timestamp: now,
						Content:   cmdStr,
					}
					if isPortHciMode(portName) && len(sendData) > 0 {
						if explainLines := parseHciCommandLines(sendData); len(explainLines) > 0 {
							for _, l := range explainLines {
								logChan <- LogMessage{
									PortName:  portName,
									Direction: "TX",
									ColorCode: "\033[1;35m",
									Timestamp: now,
									Content:   l,
								}
							}
						}
					}
				}
			}
			return true
		})

		if count > 1 {
			logChan <- LogMessage{
				PortName:  "SYS",
				Direction: "SYS",
				ColorCode: "\033[1;37m",
				Timestamp: time.Now(),
				Content:   fmt.Sprintf("📢 [广播命令 -> %d 个串口]: %s", count, cmdStr),
			}
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
