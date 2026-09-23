package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// =======================================================================
// BLE HCI Protocol Parser & Preset Commands Engine
// =======================================================================

type HciCmdInfo struct {
	Name string
	Desc string
}

var hciCmdMap = map[uint16]HciCmdInfo{
	// OGF 0x01: Link Control
	0x0406: {"HCI_Disconnect", "断开连接"},
	// OGF 0x03: Controller & Baseband
	0x0C01: {"HCI_Set_Event_Mask", "设置事件掩码"},
	0x0C03: {"HCI_Reset", "复位 BLE 控制器"},
	0x0C14: {"HCI_Read_Local_Name", "读取设备名称"},
	0x0C13: {"HCI_Change_Local_Name", "修改设备名称"},
	0x0C2D: {"HCI_Write_Extended_Inquiry_Response", "写入扩展广播响应"},
	0x0C33: {"HCI_Host_Buffer_Size", "主机缓冲区大小"},
	// OGF 0x04: Informational Parameters
	0x1001: {"HCI_Read_Local_Version_Information", "读取固件与芯片版本"},
	0x1002: {"HCI_Read_Local_Supported_Commands", "读取支持的指令集"},
	0x1003: {"HCI_Read_Local_Supported_Features", "读取支持的特性"},
	0x1009: {"HCI_Read_BD_ADDR", "读取设备 MAC 地址"},
	// OGF 0x08: LE Controller Commands
	0x2001: {"HCI_LE_Set_Event_Mask", "设置 LE 事件掩码"},
	0x2002: {"HCI_LE_Read_Buffer_Size", "读取 LE 缓冲区大小"},
	0x2003: {"HCI_LE_Read_Local_Supported_Features", "读取 LE 特性"},
	0x2005: {"HCI_LE_Set_Random_Address", "设置随机静态地址"},
	0x2006: {"HCI_LE_Set_Advertising_Parameters", "设置广播参数"},
	0x2007: {"HCI_LE_Read_Advertising_Physical_Channel_Tx_Power", "读取广播发射功率"},
	0x2008: {"HCI_LE_Set_Advertising_Data", "设置广播数据"},
	0x2009: {"HCI_LE_Set_Scan_Response_Data", "设置扫描响应数据"},
	0x200A: {"HCI_LE_Set_Advertising_Enable", "开启/关闭广播"},
	0x200B: {"HCI_LE_Set_Scan_Parameters", "设置扫描参数"},
	0x200C: {"HCI_LE_Set_Scan_Enable", "开启/关闭扫描"},
	0x200D: {"HCI_LE_Create_Connection", "建立 BLE 连接"},
	0x200E: {"HCI_LE_Create_Connection_Cancel", "取消建立连接"},
	0x200F: {"HCI_LE_Read_Filter_Accept_List_Size", "读取过滤列表大小"},
	0x2010: {"HCI_LE_Clear_Filter_Accept_List", "清空过滤列表"},
	0x2011: {"HCI_LE_Add_Device_To_Filter_Accept_List", "添加设备到过滤列表"},
	0x2012: {"HCI_LE_Remove_Device_From_Filter_Accept_List", "从过滤列表移除设备"},
	0x2013: {"HCI_LE_Connection_Update", "更新连接参数"},
	0x2014: {"HCI_LE_Set_Host_Channel_Classification", "设置主机信道分类"},
	0x2015: {"HCI_LE_Read_Channel_Map", "读取信道图"},
	0x2016: {"HCI_LE_Read_Remote_Features", "读取对端特性"},
	0x2017: {"HCI_LE_Encrypt", "AES-128 加密"},
	0x2018: {"HCI_LE_Rand", "获取随机数"},
	0x201D: {"HCI_LE_Receiver_Test", "射频接收测试 (DTM RX)"},
	0x201E: {"HCI_LE_Transmitter_Test", "射频发射测试 (DTM TX)"},
	0x201F: {"HCI_LE_Test_End", "结束射频测试 (DTM End)"},
	0x202B: {"HCI_LE_Set_Default_PHY", "设置默认 PHY"},
	0x2032: {"HCI_LE_Set_Extended_Advertising_Parameters", "设置扩展广播参数"},
	0x2035: {"HCI_LE_Set_Extended_Advertising_Enable", "开启/关闭扩展广播"},
	0x2037: {"HCI_LE_Set_Extended_Scan_Parameters", "设置扩展扫描参数"},
	0x2039: {"HCI_LE_Set_Extended_Scan_Enable", "开启/关闭扩展扫描"},
	0x203B: {"HCI_LE_Extended_Create_Connection", "建立扩展连接"},
	// OGF 0x3F: Vendor Specific (NXP)
	0xFD2D: {"HCI_NXP_Set_Tx_Power", "NXP 设置发射功率 (Config TX Power)"},
}

var hciOGFNames = map[uint8]string{
	0x01: "Link Control",
	0x02: "Link Policy",
	0x03: "Controller & Baseband",
	0x04: "Informational Parameters",
	0x05: "Status Parameters",
	0x06: "Testing Commands",
	0x08: "LE Controller",
	0x3F: "Vendor Specific (原厂私有)",
}

var hciErrorCodes = map[uint8]string{
	0x00: "Success (成功)",
	0x01: "Unknown HCI Command (未知指令)",
	0x02: "Unknown Connection Identifier (未知连接标识)",
	0x03: "Hardware Failure (硬件故障)",
	0x04: "Page Timeout (寻呼超时)",
	0x05: "Authentication Failure (认证失败)",
	0x06: "PIN or Key Missing (缺失密钥)",
	0x07: "Memory Capacity Exceeded (内存溢出)",
	0x08: "Connection Timeout (连接超时)",
	0x09: "Connection Limit Exceeded (超出连接数限制)",
	0x0C: "Command Disallowed (命令被拒绝/当前状态不支持)",
	0x0D: "Connection Rejected Due To Limited Resources (资源不足拒绝连接)",
	0x0E: "Connection Rejected Due To Security Reasons (安全原因拒绝连接)",
	0x0F: "Connection Rejected Due To Unacceptable BD_ADDR (MAC地址不合法拒绝)",
	0x10: "Connection Accept Timeout Exceeded (连接接受超时)",
	0x11: "Unsupported Feature Or Parameter Value (不支持的特性或参数值)",
	0x12: "Invalid HCI Command Parameters (非法参数)",
	0x13: "Remote User Terminated Connection (对端用户主动断开)",
	0x16: "Connection Terminated By Local Host (本地主动断开连接)",
	0x1F: "Unspecified Error (未指定错误)",
	0x22: "LMP/LL Response Timeout (链路响应超时)",
	0x28: "Instant Passed (瞬间已过)",
	0x3A: "Controller Busy (控制器繁忙)",
	0x3C: "Advertising Timeout (广播超时)",
	0x3E: "Connection Failed to be Established (连接建立失败)",
}

var hciEventNames = map[uint8]string{
	0x05: "HCI_Disconnection_Complete",
	0x0E: "HCI_Command_Complete",
	0x0F: "HCI_Command_Status",
	0x10: "HCI_Hardware_Error",
	0x13: "HCI_Number_Of_Completed_Packets",
	0x1A: "HCI_Data_Buffer_Overflow",
	0x3E: "HCI_LE_Meta_Event",
	0xFF: "HCI_Vendor_Specific_Event",
}

var hciLESubeventNames = map[uint8]string{
	0x01: "HCI_LE_Connection_Complete",
	0x02: "HCI_LE_Advertising_Report",
	0x03: "HCI_LE_Connection_Update_Complete",
	0x04: "HCI_LE_Read_Remote_Features_Complete",
	0x05: "HCI_LE_Long_Term_Key_Request",
	0x0A: "HCI_LE_Enhanced_Connection_Complete",
	0x0D: "HCI_LE_Extended_Advertising_Report",
}

func getHciErrorDesc(code uint8) string {
	if s, ok := hciErrorCodes[code]; ok {
		return s
	}
	return fmt.Sprintf("Error 0x%02X", code)
}

func getHciCmdName(opcode uint16) string {
	if info, ok := hciCmdMap[opcode]; ok {
		return info.Name + " (" + info.Desc + ")"
	}
	ogf := uint8(opcode >> 10)
	ocf := uint16(opcode & 0x03FF)
	ogfName := hciOGFNames[ogf]
	if ogfName == "" {
		ogfName = fmt.Sprintf("OGF 0x%02X", ogf)
	}
	return fmt.Sprintf("Unknown 0x%04X [%s, OCF 0x%03X]", opcode, ogfName, ocf)
}

func getHciEventName(code uint8) string {
	if s, ok := hciEventNames[code]; ok {
		return s
	}
	return fmt.Sprintf("Unknown_Event_0x%02X", code)
}

func getHciLESubeventName(sub uint8) string {
	if s, ok := hciLESubeventNames[sub]; ok {
		return s
	}
	return fmt.Sprintf("Unknown_Subevent_0x%02X", sub)
}

func getBluetoothVersion(v uint8) string {
	switch v {
	case 0:
		return "1.0b"
	case 1:
		return "1.1"
	case 2:
		return "1.2"
	case 3:
		return "2.0+EDR"
	case 4:
		return "2.1+EDR"
	case 5:
		return "3.0+HS"
	case 6:
		return "4.0"
	case 7:
		return "4.1"
	case 8:
		return "4.2"
	case 9:
		return "5.0"
	case 10:
		return "5.1"
	case 11:
		return "5.2"
	case 12:
		return "5.3"
	case 13:
		return "5.4"
	default:
		return fmt.Sprintf("Core %d.x", v)
	}
}

func getManufacturerName(id uint16) string {
	switch id {
	case 0x000F:
		return "Broadcom"
	case 0x0013:
		return "Texas Instruments (TI)"
	case 0x001D:
		return "Qualcomm"
	case 0x0025:
		return "NXP Semiconductors"
	case 0x0046:
		return "MediaTek"
	case 0x0059:
		return "Nordic Semiconductor"
	case 0x005D:
		return "Realtek"
	case 0x004C:
		return "Apple"
	case 0x00E0:
		return "Google"
	case 0x027D:
		return "Espressif"
	default:
		return fmt.Sprintf("Company ID 0x%04X", id)
	}
}

func formatHexBytes(b []byte) string {
	var sb strings.Builder
	for i, v := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02X", v)
	}
	return sb.String()
}

func parseHciCommandLines(packet []byte) []string {
	if len(packet) < 4 || packet[0] != 0x01 {
		return nil
	}
	opcode := uint16(packet[1]) | (uint16(packet[2]) << 8)
	paramLen := int(packet[3])
	ogf := uint8(opcode >> 10)
	ocf := uint16(opcode & 0x03FF)

	cmdInfo, exists := hciCmdMap[opcode]
	cmdName := "Unknown Command"
	if exists {
		cmdName = cmdInfo.Name + " (" + cmdInfo.Desc + ")"
	}
	ogfName := hciOGFNames[ogf]
	if ogfName == "" {
		ogfName = fmt.Sprintf("OGF 0x%02X", ogf)
	}

	header := fmt.Sprintf("💡 [HCI CMD] %s (Opcode: 0x%04X, OGF: 0x%02X %s, OCF: 0x%04X, ParamLen: %d)",
		cmdName, opcode, ogf, ogfName, ocf, paramLen)
	lines := []string{header}

	params := packet[4:]
	if len(params) > 0 {
		switch opcode {
		case 0x200A: // HCI_LE_Set_Advertising_Enable
			if len(params) >= 1 {
				enStr := "0x00 [Disabled / 关闭]"
				if params[0] == 1 {
					enStr = "0x01 [Enabled / 开启]"
				}
				lines = append(lines, fmt.Sprintf("   └─ Advertising_Enable: %s", enStr))
			}
		case 0x200C: // HCI_LE_Set_Scan_Enable
			if len(params) >= 1 {
				enStr := "0x00 [Disabled / 关闭]"
				if params[0] == 1 {
					enStr = "0x01 [Enabled / 开启]"
				}
				dupStr := "0x00 [Disabled]"
				if len(params) >= 2 && params[1] == 1 {
					dupStr = "0x01 [Enabled]"
				}
				lines = append(lines, fmt.Sprintf("   ├─ LE_Scan_Enable: %s", enStr))
				lines = append(lines, fmt.Sprintf("   └─ Filter_Duplicates: %s", dupStr))
			}
		case 0x201E: // HCI_LE_Transmitter_Test (DTM TX)
			if len(params) >= 3 {
				lines = append(lines, fmt.Sprintf("   ├─ TX_Channel: %d (2402 + %d*2 MHz)", params[0], params[0]))
				lines = append(lines, fmt.Sprintf("   ├─ Length_Of_Test_Data: %d bytes", params[1]))
				lines = append(lines, fmt.Sprintf("   └─ Packet_Payload: 0x%02X", params[2]))
			}
		case 0x201D: // HCI_LE_Receiver_Test (DTM RX)
			if len(params) >= 1 {
				lines = append(lines, fmt.Sprintf("   └─ RX_Channel: %d (2402 + %d*2 MHz)", params[0], params[0]))
			}
		case 0xFD2D: // HCI_NXP_Set_Tx_Power
			if len(params) >= 2 {
				chanTypeStr := "0x00 [Connection / 连接通道 (KW38同时控制两种通道)]"
				if params[1] == 0x01 {
					chanTypeStr = "0x01 [Advertising / 广播通道 (KW45同时控制两种通道)]"
				} else if params[1] != 0 {
					chanTypeStr = fmt.Sprintf("0x%02X [Channel Type]", params[1])
				}
				lines = append(lines, fmt.Sprintf("   ├─ Tx_Power: 0x%02X (%d) [KW45: 0~10dBm (0x00~0x0A), KW38: 0~5dBm (0x00~0x14)]", params[0], params[0]))
				lines = append(lines, fmt.Sprintf("   └─ Channel_Type: %s", chanTypeStr))
			}
		default:
			if len(params) <= 16 {
				lines = append(lines, fmt.Sprintf("   └─ Parameters: [%s]", formatHexBytes(params)))
			}
		}
	}
	return lines
}

func parseHciEventLines(packet []byte) []string {
	if len(packet) < 3 || packet[0] != 0x04 {
		return nil
	}
	evtCode := packet[1]
	paramLen := int(packet[2])
	params := packet[3:]
	if len(params) > paramLen {
		params = params[:paramLen]
	}

	var lines []string

	switch evtCode {
	case 0x0E: // HCI_Command_Complete
		if len(params) < 3 {
			lines = append(lines, fmt.Sprintf("💡 [HCI EVT] HCI_Command_Complete (Len: %d)", paramLen))
			return lines
		}
		numPackets := params[0]
		opcode := uint16(params[1]) | (uint16(params[2]) << 8)
		cmdName := getHciCmdName(opcode)

		lines = append(lines, fmt.Sprintf("💡 [HCI EVT] HCI_Command_Complete (Len: %d)", paramLen))
		lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Num_HCI_Command_Packets: %d", numPackets, numPackets))
		lines = append(lines, fmt.Sprintf("   ├─ [%02X %02X] Opcode: 0x%04X -> %s", params[1], params[2], opcode, cmdName))

		if len(params) >= 4 {
			status := params[3]
			statusDesc := getHciErrorDesc(status)
			retParams := params[4:]

			if status == 0 && opcode == 0x1009 && len(retParams) == 6 { // HCI_Read_BD_ADDR
				macStr := fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
					retParams[5], retParams[4], retParams[3], retParams[2], retParams[1], retParams[0])
				lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
				lines = append(lines, fmt.Sprintf("   └─ [%s] BD_ADDR: %s", formatHexBytes(retParams), macStr))
			} else if status == 0 && opcode == 0x1001 && len(retParams) >= 8 { // HCI_Read_Local_Version_Information
				hciVer := retParams[0]
				hciSubver := uint16(retParams[1]) | (uint16(retParams[2]) << 8)
				lmpVer := retParams[3]
				mfg := uint16(retParams[4]) | (uint16(retParams[5]) << 8)
				lmpSubver := uint16(retParams[6]) | (uint16(retParams[7]) << 8)
				lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
				lines = append(lines, fmt.Sprintf("   ├─ HCI Version: %s, Subversion: 0x%04X", getBluetoothVersion(hciVer), hciSubver))
				lines = append(lines, fmt.Sprintf("   ├─ LMP Version: %s, Subversion: 0x%04X", getBluetoothVersion(lmpVer), lmpSubver))
				lines = append(lines, fmt.Sprintf("   └─ Manufacturer: 0x%04X (%s)", mfg, getManufacturerName(mfg)))
			} else if status == 0 && opcode == 0x2002 && len(retParams) >= 3 { // HCI_LE_Read_Buffer_Size
				aclLen := uint16(retParams[0]) | (uint16(retParams[1]) << 8)
				numBuf := retParams[2]
				lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
				lines = append(lines, fmt.Sprintf("   └─ HC_LE_Data_Packet_Length: %d bytes, Total_Num_LE_Data_Packets: %d", aclLen, numBuf))
			} else if status == 0 && opcode == 0x201F && len(retParams) >= 2 { // HCI_LE_Test_End (DTM End)
				totalPkt := uint16(retParams[0]) | (uint16(retParams[1]) << 8)
				lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
				lines = append(lines, fmt.Sprintf("   └─ Number_Of_Packets (收发测试封包总数): %d", totalPkt))
			} else if opcode == 0xFD2D { // HCI_NXP_Set_Tx_Power
				if status == 0 {
					lines = append(lines, fmt.Sprintf("   └─ [%02X]    Status: 0x%02X (Success / Config TX power Command succeeded)", status, status))
				} else {
					lines = append(lines, fmt.Sprintf("   └─ [%02X]    Status: 0x%02X (Failed / Config TX power Command failed: %s)", status, status, statusDesc))
				}
			} else if len(retParams) > 0 {
				lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
				lines = append(lines, fmt.Sprintf("   └─ Return_Parameters: [%s]", formatHexBytes(retParams)))
			} else {
				lines = append(lines, fmt.Sprintf("   └─ [%02X]    Status: 0x%02X (%s)", status, status, statusDesc))
			}
		}

	case 0x0F: // HCI_Command_Status
		if len(params) >= 4 {
			status := params[0]
			numPackets := params[1]
			opcode := uint16(params[2]) | (uint16(params[3]) << 8)
			lines = append(lines, fmt.Sprintf("💡 [HCI EVT] HCI_Command_Status (Len: %d)", paramLen))
			lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Status: 0x%02X (%s)", status, status, getHciErrorDesc(status)))
			lines = append(lines, fmt.Sprintf("   ├─ [%02X]    Num_HCI_Command_Packets: %d", numPackets, numPackets))
			lines = append(lines, fmt.Sprintf("   └─ [%02X %02X] Opcode: 0x%04X -> %s", params[2], params[3], opcode, getHciCmdName(opcode)))
		}

	case 0x05: // HCI_Disconnection_Complete
		if len(params) >= 4 {
			status := params[0]
			handle := uint16(params[1]) | (uint16(params[2]) << 8)
			reason := params[3]
			lines = append(lines, fmt.Sprintf("💡 [HCI EVT] HCI_Disconnection_Complete (Len: %d)", paramLen))
			lines = append(lines, fmt.Sprintf("   ├─ Status: 0x%02X (%s)", status, getHciErrorDesc(status)))
			lines = append(lines, fmt.Sprintf("   ├─ Connection_Handle: 0x%04X", handle))
			lines = append(lines, fmt.Sprintf("   └─ Reason: 0x%02X (%s)", reason, getHciErrorDesc(reason)))
		}

	case 0x3E: // HCI_LE_Meta_Event
		if len(params) >= 1 {
			subevent := params[0]
			subName := getHciLESubeventName(subevent)
			lines = append(lines, fmt.Sprintf("💡 [HCI EVT] HCI_LE_Meta_Event (Subevent: 0x%02X -> %s)", subevent, subName))

			if subevent == 0x01 && len(params) >= 19 { // HCI_LE_Connection_Complete
				status := params[1]
				handle := uint16(params[2]) | (uint16(params[3]) << 8)
				role := params[4]
				peerAddrType := params[5]
				peerAddr := fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
					params[11], params[10], params[9], params[8], params[7], params[6])
				roleStr := "Central (Master/主端)"
				if role == 1 {
					roleStr = "Peripheral (Slave/从端)"
				}
				lines = append(lines, fmt.Sprintf("   ├─ Status: 0x%02X (%s)", status, getHciErrorDesc(status)))
				lines = append(lines, fmt.Sprintf("   ├─ Connection_Handle: 0x%04X", handle))
				lines = append(lines, fmt.Sprintf("   ├─ Role: %s", roleStr))
				lines = append(lines, fmt.Sprintf("   ├─ Peer_Address_Type: 0x%02X", peerAddrType))
				lines = append(lines, fmt.Sprintf("   └─ Peer_Address: %s", peerAddr))
			} else if subevent == 0x02 && len(params) >= 2 { // HCI_LE_Advertising_Report
				numReports := params[1]
				lines = append(lines, fmt.Sprintf("   ├─ Num_Reports: %d", numReports))
				if len(params) >= 10 {
					evtType := params[2]
					addrType := params[3]
					addr := fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
						params[9], params[8], params[7], params[6], params[5], params[4])
					lines = append(lines, fmt.Sprintf("   ├─ Event_Type: 0x%02X, Address_Type: 0x%02X", evtType, addrType))
					lines = append(lines, fmt.Sprintf("   ├─ Address: %s", addr))
					dataLen := int(params[10])
					if len(params) >= 11+dataLen {
						advData := params[11 : 11+dataLen]
						lines = append(lines, fmt.Sprintf("   ├─ Data_Length: %d, Data: [%s]", dataLen, formatHexBytes(advData)))
						if len(params) >= 12+dataLen {
							rssi := int8(params[11+dataLen])
							lines = append(lines, fmt.Sprintf("   └─ RSSI: %d dBm", rssi))
						}
					}
				}
			} else {
				lines = append(lines, fmt.Sprintf("   └─ Subevent_Parameters: [%s]", formatHexBytes(params[1:])))
			}
		}

	default:
		evtName := getHciEventName(evtCode)
		lines = append(lines, fmt.Sprintf("💡 [HCI EVT] %s (Code: 0x%02X, Len: %d)", evtName, evtCode, paramLen))
		if len(params) > 0 {
			lines = append(lines, fmt.Sprintf("   └─ Parameters: [%s]", formatHexBytes(params)))
		}
	}
	return lines
}

type PresetHciCmd struct {
	Name       string
	Usage      string
	Desc       string
	ParamsHint string
	BuildFunc  func(args []string) ([]byte, string, error)
}

var presetHciCmdOrder = []string{
	"/cmds",
	"/reset",
	"/bdaddr",
	"/version",
	"/bufsize",
	"/adv",
	"/advparam",
	"/advdata",
	"/scan",
	"/scanparam",
	"/dtm-tx",
	"/dtm-rx",
	"/dtm-end",
	"/txpower",
}

func parseHexOrDec(s string) (int, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseInt(s[2:], 16, 32)
		return int(v), err
	}
	v, err := strconv.Atoi(s)
	return v, err
}

func buildNxpTxPower(args []string) ([]byte, string, error) {
	if len(args) == 0 {
		return nil, "", fmt.Errorf("参数缺失! 用法: /txpower <power: 0~20> [chan_type: 0|1] 或 /txpower kw45 <power> / /txpower kw38 <power>")
	}

	chanType := 1 // 默认 1 (KW45 广播通道，可同时控制连接与广播通道)
	powerStr := args[0]
	chipHint := ""

	if strings.ToLower(args[0]) == "kw38" {
		if len(args) < 2 {
			return nil, "", fmt.Errorf("请输入 KW38 功率值 (0~20 / 0x00~0x14, 对应 0~5dBm)! 用法: /txpower kw38 <power>")
		}
		chipHint = "KW38 "
		powerStr = args[1]
		chanType = 0 // KW38 设置 0x00 可同时控制连接与广播通道
	} else if strings.ToLower(args[0]) == "kw45" {
		if len(args) < 2 {
			return nil, "", fmt.Errorf("请输入 KW45 功率值 (0~10 / 0x00~0x0A, 对应 0~10dBm)! 用法: /txpower kw45 <power>")
		}
		chipHint = "KW45 "
		powerStr = args[1]
		chanType = 1 // KW45 设置 0x01 可同时控制连接与广播通道
	} else if len(args) >= 2 {
		arg2Lower := strings.ToLower(args[1])
		if arg2Lower == "kw38" || arg2Lower == "conn" {
			chanType = 0
		} else if arg2Lower == "kw45" || arg2Lower == "adv" {
			chanType = 1
		} else {
			ct, err := parseHexOrDec(args[1])
			if err != nil || (ct != 0 && ct != 1) {
				return nil, "", fmt.Errorf("通道类型 chan_type 无效 (0: 连接通道/KW38, 1: 广播通道/KW45): %s", args[1])
			}
			chanType = ct
		}
	}

	power, err := parseHexOrDec(powerStr)
	if err != nil {
		return nil, "", fmt.Errorf("发射功率值无效: %s (%v)", powerStr, err)
	}
	if power < 0 || power > 255 {
		return nil, "", fmt.Errorf("发射功率值超出范围 0~255 (KW45: 0~10 / 0x00~0x0A, KW38: 0~20 / 0x00~0x14): %d", power)
	}

	pkt := []byte{0x01, 0x2D, 0xFD, 0x02, byte(power), byte(chanType)}
	chanDesc := "0x01 [广播通道 / KW45推荐]"
	if chanType == 0 {
		chanDesc = "0x00 [连接通道 / KW38推荐]"
	}
	return pkt, fmt.Sprintf("HCI_NXP_Set_Tx_Power (%sTx_Power: 0x%02X (%d), Channel_Type: %s)", chipHint, power, power, chanDesc), nil
}

var presetHciCmdMap = map[string]PresetHciCmd{
	"/reset": {
		Name:  "/reset",
		Usage: "/reset",
		Desc:  "复位 BLE 控制器 (HCI_Reset)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x03, 0x0C, 0x00}, "HCI_Reset (Opcode: 0x0C03, OGF: 0x03, OCF: 0x0003, ParamLen: 0)", nil
		},
	},
	"/bdaddr": {
		Name:  "/bdaddr",
		Usage: "/bdaddr",
		Desc:  "读取设备 MAC 地址 (HCI_Read_BD_ADDR)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x09, 0x10, 0x00}, "HCI_Read_BD_ADDR (Opcode: 0x1009, OGF: 0x04, OCF: 0x0009, ParamLen: 0)", nil
		},
	},
	"/addr": {
		Name:  "/addr",
		Usage: "/addr",
		Desc:  "同 /bdaddr",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x09, 0x10, 0x00}, "HCI_Read_BD_ADDR (Opcode: 0x1009, OGF: 0x04, OCF: 0x0009, ParamLen: 0)", nil
		},
	},
	"/version": {
		Name:  "/version",
		Usage: "/version",
		Desc:  "读取固件与芯片版本信息 (HCI_Read_Local_Version_Information)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x01, 0x10, 0x00}, "HCI_Read_Local_Version_Information (Opcode: 0x1001, OGF: 0x04, OCF: 0x0001, ParamLen: 0)", nil
		},
	},
	"/bufsize": {
		Name:  "/bufsize",
		Usage: "/bufsize",
		Desc:  "读取 LE 控制器数据缓冲区大小 (HCI_LE_Read_Buffer_Size)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x02, 0x20, 0x00}, "HCI_LE_Read_Buffer_Size (Opcode: 0x2002, OGF: 0x08, OCF: 0x0002, ParamLen: 0)", nil
		},
	},
	"/adv": {
		Name:  "/adv",
		Usage: "/adv <on|off> (例如: /adv on, /adv off)",
		Desc:  "开启/关闭广播 (HCI_LE_Set_Advertising_Enable)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			if len(args) == 0 {
				return nil, "", fmt.Errorf("参数缺失! 用法: /adv <on|off>")
			}
			val := strings.ToLower(args[0])
			if val == "on" || val == "1" || val == "enable" {
				return []byte{0x01, 0x0A, 0x20, 0x01, 0x01}, "HCI_LE_Set_Advertising_Enable (ParamLen: 1, Advertising_Enable: 0x01 [ON])", nil
			} else if val == "off" || val == "0" || val == "disable" {
				return []byte{0x01, 0x0A, 0x20, 0x01, 0x00}, "HCI_LE_Set_Advertising_Enable (ParamLen: 1, Advertising_Enable: 0x00 [OFF])", nil
			}
			return nil, "", fmt.Errorf("未知参数 %q! 请指定 on 或 off (例如: /adv on)", args[0])
		},
	},
	"/advparam": {
		Name:  "/advparam",
		Usage: "/advparam [interval_ms: 20~10240, 默认100]",
		Desc:  "设置广播参数 (HCI_LE_Set_Advertising_Parameters)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			ms := 100
			if len(args) > 0 {
				if v, err := strconv.Atoi(args[0]); err == nil {
					if v < 20 || v > 10240 {
						return nil, "", fmt.Errorf("广播间隔范围应在 20~10240 ms 之间 (当前: %d)", v)
					}
					ms = v
				} else {
					return nil, "", fmt.Errorf("非法时间数值 %q", args[0])
				}
			}
			slots := uint16(float64(ms) / 0.625)
			minL := byte(slots & 0xFF)
			minH := byte((slots >> 8) & 0xFF)
			pkt := []byte{0x01, 0x06, 0x20, 0x0F, minL, minH, minL, minH, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x07, 0x00}
			return pkt, fmt.Sprintf("HCI_LE_Set_Advertising_Parameters (Interval: %d ms, Type: ADV_IND, ChannelMap: 7)", ms), nil
		},
	},
	"/advdata": {
		Name:  "/advdata",
		Usage: "/advdata <hex...> (例如: /advdata 02 01 06 05 09 42 4C 45)",
		Desc:  "设置广播 Payload (HCI_LE_Set_Advertising_Data)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			if len(args) == 0 {
				return nil, "", fmt.Errorf("参数缺失! 请输入十六进制广播数据，例如: /advdata 02 01 06")
			}
			raw, err := parseHexInput(strings.Join(args, " "))
			if err != nil {
				return nil, "", fmt.Errorf("广播数据 Hex 解析失败: %v", err)
			}
			if len(raw) > 31 {
				return nil, "", fmt.Errorf("广播数据长度不能超过 31 字节 (当前: %d 字节)", len(raw))
			}
			payload := make([]byte, 31)
			copy(payload, raw)
			pkt := append([]byte{0x01, 0x08, 0x20, 0x20, byte(len(raw))}, payload...)
			return pkt, fmt.Sprintf("HCI_LE_Set_Advertising_Data (DataLen: %d, Data: [%s])", len(raw), formatHexBytes(raw)), nil
		},
	},
	"/scan": {
		Name:  "/scan",
		Usage: "/scan <on|off> [filter: 0|1, 默认0]",
		Desc:  "开启/关闭扫描 (HCI_LE_Set_Scan_Enable)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			if len(args) == 0 {
				return nil, "", fmt.Errorf("参数缺失! 用法: /scan <on|off> [filter: 0|1]")
			}
			val := strings.ToLower(args[0])
			en := byte(0)
			enDesc := "OFF"
			if val == "on" || val == "1" || val == "enable" {
				en = 1
				enDesc = "ON"
			} else if val == "off" || val == "0" || val == "disable" {
				en = 0
				enDesc = "OFF"
			} else {
				return nil, "", fmt.Errorf("未知参数 %q! 请指定 on 或 off (例如: /scan on)", args[0])
			}
			filter := byte(0)
			if len(args) > 1 && (args[1] == "1" || strings.ToLower(args[1]) == "filter") {
				filter = 1
			}
			pkt := []byte{0x01, 0x0C, 0x20, 0x02, en, filter}
			return pkt, fmt.Sprintf("HCI_LE_Set_Scan_Enable (Scan: %s, FilterDuplicates: %d)", enDesc, filter), nil
		},
	},
	"/scanparam": {
		Name:  "/scanparam",
		Usage: "/scanparam [active|passive] [interval_ms] [window_ms]",
		Desc:  "设置扫描参数 (HCI_LE_Set_Scan_Parameters)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			scanType := byte(1) // active
			scanDesc := "Active"
			intMs := 100
			winMs := 50
			if len(args) > 0 && strings.ToLower(args[0]) == "passive" {
				scanType = 0
				scanDesc = "Passive"
			}
			if len(args) > 1 {
				if v, err := strconv.Atoi(args[1]); err == nil && v >= 3 {
					intMs = v
				}
			}
			if len(args) > 2 {
				if v, err := strconv.Atoi(args[2]); err == nil && v >= 3 {
					winMs = v
				}
			}
			if winMs > intMs {
				winMs = intMs
			}
			intSlots := uint16(float64(intMs) / 0.625)
			winSlots := uint16(float64(winMs) / 0.625)
			pkt := []byte{0x01, 0x0B, 0x20, 0x07, scanType, byte(intSlots & 0xFF), byte(intSlots >> 8), byte(winSlots & 0xFF), byte(winSlots >> 8), 0x00, 0x00}
			return pkt, fmt.Sprintf("HCI_LE_Set_Scan_Parameters (Type: %s, Interval: %d ms, Window: %d ms)", scanDesc, intMs, winMs), nil
		},
	},
	"/dtm-tx": {
		Name:  "/dtm-tx",
		Usage: "/dtm-tx <chan: 0~39> [len: 0~255, 默认37] [payload: 0~7, 默认0 (PRBS9)]",
		Desc:  "开启射频发射测试 (HCI_LE_Transmitter_Test)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			if len(args) == 0 {
				return nil, "", fmt.Errorf("参数缺失! 用法: /dtm-tx <chan: 0~39> [len: 0~255] [type: 0~7]")
			}
			ch, err := strconv.Atoi(args[0])
			if err != nil || ch < 0 || ch > 39 {
				return nil, "", fmt.Errorf("信道 chan 必须在 0~39 之间 (当前输入: %s)", args[0])
			}
			dataLen := 37
			if len(args) > 1 {
				if l, err := strconv.Atoi(args[1]); err == nil && l >= 0 && l <= 255 {
					dataLen = l
				}
			}
			pType := 0
			if len(args) > 2 {
				if pt, err := strconv.Atoi(args[2]); err == nil && pt >= 0 && pt <= 7 {
					pType = pt
				}
			}
			pkt := []byte{0x01, 0x1E, 0x20, 0x03, byte(ch), byte(dataLen), byte(pType)}
			return pkt, fmt.Sprintf("HCI_LE_Transmitter_Test (Channel: %d, Length: %d bytes, Payload: 0x%02X)", ch, dataLen, pType), nil
		},
	},
	"/dtm-rx": {
		Name:  "/dtm-rx",
		Usage: "/dtm-rx <chan: 0~39>",
		Desc:  "开启射频接收测试 (HCI_LE_Receiver_Test)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			if len(args) == 0 {
				return nil, "", fmt.Errorf("参数缺失! 用法: /dtm-rx <chan: 0~39>")
			}
			ch, err := strconv.Atoi(args[0])
			if err != nil || ch < 0 || ch > 39 {
				return nil, "", fmt.Errorf("信道 chan 必须在 0~39 之间 (当前输入: %s)", args[0])
			}
			pkt := []byte{0x01, 0x1D, 0x20, 0x01, byte(ch)}
			return pkt, fmt.Sprintf("HCI_LE_Receiver_Test (Channel: %d [2402 + %d*2 MHz])", ch, ch), nil
		},
	},
	"/dtm-end": {
		Name:  "/dtm-end",
		Usage: "/dtm-end",
		Desc:  "结束射频测试并获取封包统计 (HCI_LE_Test_End)",
		BuildFunc: func(args []string) ([]byte, string, error) {
			return []byte{0x01, 0x1F, 0x20, 0x00}, "HCI_LE_Test_End (Opcode: 0x201F, OGF: 0x08, OCF: 0x001F, ParamLen: 0)", nil
		},
	},
	"/txpower": {
		Name:  "/txpower",
		Usage: "/txpower <power: 0~20> [chan_type: 0|1] 或 /txpower kw45 <power> / /txpower kw38 <power>",
		Desc:  "NXP 设置发射功率 (HCI_NXP_Set_Tx_Power 01 2D FD 02 XX YY)",
		BuildFunc: buildNxpTxPower,
	},
	"/nxp-txpower": {
		Name:  "/nxp-txpower",
		Usage: "/nxp-txpower <power: 0~20> [chan_type: 0|1]",
		Desc:  "同 /txpower",
		BuildFunc: buildNxpTxPower,
	},
}

func getHciCmdParamHint(verb string) string {
	if cmd, ok := presetHciCmdMap[verb]; ok {
		return cmd.Usage
	}
	return ""
}

func findLCP(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

func printHciCommandsCheatSheet(logChan chan<- LogMessage) {
	lines := []string{
		"==================================== 📡 BLE HCI 预设指令集 ====================================",
		"【系统与基础信息】",
		"  /reset                     - 复位 BLE 控制器 (HCI_Reset)",
		"  /bdaddr                    - 读取设备 MAC 地址 (HCI_Read_BD_ADDR)",
		"  /version                   - 读取 HCI/LMP 固件版本信息 (HCI_Read_Local_Version_Information)",
		"  /bufsize                   - 读取 LE 控制器数据缓冲区大小 (HCI_LE_Read_Buffer_Size)",
		"",
		"【广播管理 (Advertising)】",
		"  /adv <on|off>              - 开启/关闭广播 (例如: /adv on, /adv off)",
		"  /advparam [interval_ms]    - 设置广播参数 (默认 100ms, 可选间隔: 20~10240ms)",
		"  /advdata <hex...>          - 设置广播 Payload (例如: /advdata 02 01 06 05 09 42 4C 45)",
		"",
		"【扫描管理 (Scanning)】",
		"  /scan <on|off> [filter]    - 开启/关闭扫描 (filter: 0=不过滤重复, 1=过滤重复, 默认 0)",
		"  /scanparam [active|pass]   - 设置扫描参数 (默认 active 模式, 间隔 100ms, 窗口 50ms)",
		"",
		"【RF 射频测试 (DTM)】",
		"  /dtm-tx <chan> [len] [pt]  - 开启射频发射测试 (chan: 0~39, len: 默认37, pt: 0=PRBS9)",
		"  /dtm-rx <chan>             - 开启射频接收测试 (chan: 0~39)",
		"  /dtm-end                   - 结束射频测试并返回发包/收包统计 (HCI_LE_Test_End)",
		"",
		"【NXP 原厂私有指令 (Vendor Specific)】",
		"  /txpower <power> [chan]    - NXP 设置发射功率 (KW45: 0~10 选通道1; KW38: 0~20 选通道0)",
		"                               (支持芯片前缀: /txpower kw45 8 或 /txpower kw38 5)",
		"                               ⚠️ 注: KW45 每次设置功率前建议先执行 /reset",
		"==============================================================================================",
		"💡 提示: 直接输入命令按回车下发; 按 Tab 键可自动补全命令及参数; 原始 16 进制依然支持 (如 01 2D FD 02 08 01)",
	}
	now := time.Now()
	for _, l := range lines {
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;36m",
			Timestamp: now,
			Content:   l,
		}
	}
}

func handleHciSlashCommand(targetName string, targetPort serial.Port, isTargeted bool, cmdStr string, activePorts *sync.Map, logChan chan<- LogMessage) {
	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return
	}
	verb := strings.ToLower(fields[0])

	if verb == "/cmds" || verb == "/help" || verb == "/h" {
		printHciCommandsCheatSheet(logChan)
		return
	}

	preset, exists := presetHciCmdMap[verb]
	if !exists {
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;31m",
			Timestamp: time.Now(),
			Content:   fmt.Sprintf("❌ 未知 HCI 快捷指令: %s (输入 /cmds 查看支持的预设指令列表)", verb),
		}
		return
	}

	rawBytes, explain, err := preset.BuildFunc(fields[1:])
	if err != nil {
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;31m",
			Timestamp: time.Now(),
			Content:   fmt.Sprintf("❌ [参数错误]: %v\n💡 用法: %s", err, preset.Usage),
		}
		return
	}

	type sendTarget struct {
		name string
		port serial.Port
	}
	var targets []sendTarget

	if isTargeted && targetPort != nil {
		targets = append(targets, sendTarget{name: targetName, port: targetPort})
	} else {
		var hciTargets []sendTarget
		var allTargets []sendTarget
		activePorts.Range(func(key, value any) bool {
			pName, ok1 := key.(string)
			pObj, ok2 := value.(serial.Port)
			if ok1 && ok2 {
				allTargets = append(allTargets, sendTarget{name: pName, port: pObj})
				if isPortHciMode(pName) {
					hciTargets = append(hciTargets, sendTarget{name: pName, port: pObj})
				}
			}
			return true
		})

		if len(hciTargets) > 0 {
			targets = hciTargets
		} else {
			targets = allTargets
		}
	}

	if len(targets) == 0 {
		logChan <- LogMessage{
			PortName:  "SYS",
			Direction: "SYS",
			ColorCode: "\033[1;33m",
			Timestamp: time.Now(),
			Content:   fmt.Sprintf("⚠️ 当前无可用已连接串口，HCI 指令未下发: %s", cmdStr),
		}
		return
	}

	hexStr := formatHexBytes(rawBytes)
	for _, tgt := range targets {
		_, writeErr := tgt.port.Write(rawBytes)
		now := time.Now()
		if writeErr != nil {
			logChan <- LogMessage{
				PortName:  tgt.name,
				Direction: "SYS",
				ColorCode: "\033[1;31m",
				Timestamp: now,
				Content:   fmt.Sprintf("❌ [发送失败 -> %s]: %v", tgt.name, writeErr),
			}
		} else {
			logChan <- LogMessage{
				PortName:  tgt.name,
				Direction: "TX",
				ColorCode: "\033[1;35m",
				Timestamp: now,
				Content:   hexStr,
			}
			explainLines := parseHciCommandLines(rawBytes)
			if len(explainLines) == 0 && explain != "" {
				explainLines = []string{"💡 [HCI CMD] " + explain}
			}
			for _, l := range explainLines {
				logChan <- LogMessage{
					PortName:  tgt.name,
					Direction: "TX",
					ColorCode: "\033[1;35m",
					Timestamp: now,
					Content:   l,
				}
			}
		}
	}
}
