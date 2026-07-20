//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	className = "AnalogOutputUtilityWindow"

	wmCreate          = 0x0001
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmSetFont         = 0x0030
	wmCommand         = 0x0111
	wmAppResult       = 0x8001
	emSetSel          = 0x00B1
	emScrollCaret     = 0x00B7
	emReplaceSel      = 0x00C2
	emSetPasswordChar = 0x00CC
	bmGetCheck        = 0x00F0
	bmSetCheck        = 0x00F1
	cbAddString       = 0x0143
	cbGetCurSel       = 0x0147
	cbSetCurSel       = 0x014E

	bnClicked = 0
	enChange  = 0x0300

	bstUnchecked = 0
	bstChecked   = 1

	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	wsBorder       = 0x00800000
	wsVScroll      = 0x00200000
	wsClipChildren = 0x02000000

	wsExClientEdge    = 0x00000200
	wsExControlParent = 0x00010000

	bsPushButton    = 0x00000000
	bsDefaultButton = 0x00000001
	bsAutoCheckbox  = 0x00000003
	bsGroupBox      = 0x00000007

	esLeft        = 0x0000
	esMultiline   = 0x0004
	esPassword    = 0x0020
	esAutoVScroll = 0x0040
	esAutoHScroll = 0x0080
	esReadOnly    = 0x0800

	cbsDropdownList = 0x0003
	cbsHasStrings   = 0x0200

	colorWindow = 5

	idAddress          = 1001
	idUsername         = 1002
	idPassword         = 1003
	idTimeout          = 1004
	idShowPassword     = 1005
	idKeepPassword     = 1006
	idInvalidTLS       = 1007
	idConnect          = 1101
	idSendSettings     = 1103
	idCancel           = 1104
	idClearCredentials = 1105
	idClearLog         = 1106
	idSource1          = 1201
	idLow1             = 1202
	idHigh1            = 1203
	idLog1             = 1204
	idSource2          = 1211
	idLow2             = 1212
	idHigh2            = 1213
	idLog2             = 1214

	mbOK          = 0x00000000
	mbYesNo       = 0x00000004
	mbIconError   = 0x00000010
	mbIconWarning = 0x00000030
	mbIconInfo    = 0x00000040
	mbDefButton2  = 0x00000100
	idYes         = 6

	swShow = 5

	idiApplication = 32512
	idcArrow       = 32512

	fwNormal   = 400
	fwSemiBold = 600

	defaultCharset   = 1
	clearTypeQuality = 5
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassExW     = user32.NewProc("RegisterClassExW")
	procCreateWindowExW      = user32.NewProc("CreateWindowExW")
	procDefWindowProcW       = user32.NewProc("DefWindowProcW")
	procShowWindow           = user32.NewProc("ShowWindow")
	procUpdateWindow         = user32.NewProc("UpdateWindow")
	procGetMessageW          = user32.NewProc("GetMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessageW     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procSendMessageW         = user32.NewProc("SendMessageW")
	procMessageBoxW          = user32.NewProc("MessageBoxW")
	procEnableWindow         = user32.NewProc("EnableWindow")
	procSetWindowTextW       = user32.NewProc("SetWindowTextW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procDestroyWindow        = user32.NewProc("DestroyWindow")
	procLoadCursorW          = user32.NewProc("LoadCursorW")
	procLoadIconW            = user32.NewProc("LoadIconW")
	procSetFocus             = user32.NewProc("SetFocus")
	procInvalidateRect       = user32.NewProc("InvalidateRect")
	procIsDialogMessageW     = user32.NewProc("IsDialogMessageW")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procCreateFontW    = gdi32.NewProc("CreateFontW")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
	procGetStockObject = gdi32.NewProc("GetStockObject")
)

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type operationKind int

const (
	opConnect operationKind = iota + 1
	opSend
)

type operationResult struct {
	kind    operationKind
	status  int
	outputs []AnalogOutput
	err     error
}

type fieldError struct {
	message string
	control uintptr
}

func (e *fieldError) Error() string { return e.message }

type application struct {
	hwnd       uintptr
	hInstance  uintptr
	font       uintptr
	fontBold   uintptr
	ownFont    bool
	ownBold    bool
	startupErr error

	address      uintptr
	username     uintptr
	password     uintptr
	timeout      uintptr
	showPassword uintptr
	keepPassword uintptr
	invalidTLS   uintptr

	source [2]uintptr
	low    [2]uintptr
	high   [2]uintptr
	log    [2]uintptr

	connectButton    uintptr
	sendButton       uintptr
	cancelButton     uintptr
	clearCredentials uintptr
	clearLog         uintptr
	statusText       uintptr
	logText          uintptr

	busy      bool
	connected bool
	cancel    context.CancelFunc

	resultMu sync.Mutex
	results  []operationResult
	closing  atomic.Bool
}

var (
	appInstance     *application
	windowProcThunk = syscall.NewCallback(windowProc)
)

func main() {
	runtime.LockOSThread()
	appInstance = &application{}
	if err := appInstance.run(); err != nil {
		messageBox(0, "Analog Output Utility", err.Error(), mbOK|mbIconError)
	}
}

func (a *application) run() error {
	hInstance, _, callErr := procGetModuleHandleW.Call(0)
	if hInstance == 0 {
		return fmt.Errorf("GetModuleHandleW failed: %v", callErr)
	}
	a.hInstance = hInstance

	icon, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	classPtr := utf16Ptr(className)
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    windowProcThunk,
		Instance:   hInstance,
		Icon:       icon,
		Cursor:     cursor,
		Background: uintptr(colorWindow + 1),
		ClassName:  classPtr,
		IconSm:     icon,
	}
	atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW failed: %v", registerErr)
	}

	const windowWidth = 920
	const windowHeight = 760
	screenWidth, _, _ := procGetSystemMetrics.Call(0)
	screenHeight, _, _ := procGetSystemMetrics.Call(1)
	x := 80
	y := 60
	if int(screenWidth) > windowWidth {
		x = (int(screenWidth) - windowWidth) / 2
	}
	if int(screenHeight) > windowHeight {
		y = (int(screenHeight) - windowHeight) / 2
	}

	title := fmt.Sprintf("Analog Output Utility %s", version)
	hwnd, _, createErr := procCreateWindowExW.Call(
		uintptr(wsExControlParent),
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		uintptr(wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox|wsClipChildren),
		uintptr(x), uintptr(y), uintptr(windowWidth), uintptr(windowHeight),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		if a.startupErr != nil {
			return a.startupErr
		}
		return fmt.Errorf("CreateWindowExW failed: %v", createErr)
	}
	a.hwnd = hwnd

	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)

	var message msg
	for {
		result, _, getErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("GetMessageW failed: %v", getErr)
		}
		if result == 0 {
			break
		}
		handled, _, _ := procIsDialogMessageW.Call(hwnd, uintptr(unsafe.Pointer(&message)))
		if handled == 0 {
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
		}
	}
	return nil
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	a := appInstance
	switch message {
	case wmCreate:
		a.hwnd = hwnd
		if err := a.createControls(); err != nil {
			a.startupErr = err
			return ^uintptr(0)
		}
		return 0
	case wmCommand:
		id := int(wParam & 0xFFFF)
		code := int((wParam >> 16) & 0xFFFF)
		if code == enChange {
			switch id {
			case idAddress, idUsername, idPassword, idTimeout:
				a.invalidateConnection()
			}
		}
		if code == bnClicked {
			a.handleCommand(id)
		}
		return 0
	case wmAppResult:
		a.handleOperationResults()
		return 0
	case wmClose:
		a.closing.Store(true)
		if a.cancel != nil {
			a.cancel()
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if a.ownFont && a.font != 0 {
			procDeleteObject.Call(a.font)
		}
		if a.ownBold && a.fontBold != 0 {
			procDeleteObject.Call(a.fontBold)
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (a *application) createControls() error {
	a.font, a.ownFont = createUIFont(fwNormal)
	a.fontBold, a.ownBold = createUIFont(fwSemiBold)

	var err error
	if _, err = a.addControl(0, "BUTTON", "Instrument connection", wsChild|wsVisible|bsGroupBox, 12, 10, 880, 190, 0, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Instrument address", wsChild|wsVisible, 30, 36, 100, 22, 0, a.font); err != nil {
		return err
	}
	if a.address, err = a.addControl(wsExClientEdge, "EDIT", "", wsChild|wsVisible|wsTabStop|esLeft|esAutoHScroll, 130, 32, 500, 25, idAddress, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Timeout", wsChild|wsVisible, 650, 36, 60, 22, 0, a.font); err != nil {
		return err
	}
	if a.timeout, err = a.addControl(wsExClientEdge, "EDIT", "10", wsChild|wsVisible|wsTabStop|esLeft|esAutoHScroll, 710, 32, 55, 25, idTimeout, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "seconds", wsChild|wsVisible, 772, 36, 60, 22, 0, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Examples: 192.168.1.50, 192.168.1.50:8080, or https://instrument.local/api", wsChild|wsVisible, 130, 60, 660, 20, 0, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Username", wsChild|wsVisible, 30, 88, 95, 22, 0, a.font); err != nil {
		return err
	}
	if a.username, err = a.addControl(wsExClientEdge, "EDIT", "", wsChild|wsVisible|wsTabStop|esLeft|esAutoHScroll, 130, 84, 220, 25, idUsername, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Password", wsChild|wsVisible, 375, 88, 80, 22, 0, a.font); err != nil {
		return err
	}
	if a.password, err = a.addControl(wsExClientEdge, "EDIT", "", wsChild|wsVisible|wsTabStop|esLeft|esPassword|esAutoHScroll, 455, 84, 220, 25, idPassword, a.font); err != nil {
		return err
	}
	procSendMessageW.Call(a.password, emSetPasswordChar, uintptr('●'), 0)
	if a.showPassword, err = a.addControl(0, "BUTTON", "Show password", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 690, 84, 150, 25, idShowPassword, a.font); err != nil {
		return err
	}
	if a.keepPassword, err = a.addControl(0, "BUTTON", "Keep password until app closes", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 130, 115, 230, 24, idKeepPassword, a.font); err != nil {
		return err
	}
	setChecked(a.keepPassword, true)
	if a.invalidTLS, err = a.addControl(0, "BUTTON", "Allow an invalid HTTPS certificate", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 375, 115, 260, 24, idInvalidTLS, a.font); err != nil {
		return err
	}
	setChecked(a.invalidTLS, true)
	if _, err = a.addControl(0, "STATIC", "Credentials are kept in process memory only. HTTP Basic authentication over plain HTTP is not encrypted.", wsChild|wsVisible, 130, 141, 700, 20, 0, a.font); err != nil {
		return err
	}
	if a.connectButton, err = a.addControl(0, "BUTTON", "Connect", wsChild|wsVisible|wsTabStop|bsDefaultButton, 30, 165, 150, 28, idConnect, a.font); err != nil {
		return err
	}
	if a.sendButton, err = a.addControl(0, "BUTTON", "Send Settings", wsChild|wsVisible|wsTabStop|bsPushButton, 190, 165, 150, 28, idSendSettings, a.font); err != nil {
		return err
	}
	procEnableWindow.Call(a.sendButton, 0)
	if a.cancelButton, err = a.addControl(0, "BUTTON", "Cancel", wsChild|wsVisible|wsTabStop|bsPushButton, 350, 165, 110, 28, idCancel, a.font); err != nil {
		return err
	}
	procEnableWindow.Call(a.cancelButton, 0)
	if a.clearCredentials, err = a.addControl(0, "BUTTON", "Clear Credentials", wsChild|wsVisible|wsTabStop|bsPushButton, 470, 165, 170, 28, idClearCredentials, a.font); err != nil {
		return err
	}

	if _, err = a.addControl(0, "BUTTON", "Analog outputs", wsChild|wsVisible|bsGroupBox, 12, 208, 880, 170, 0, a.font); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Output", wsChild|wsVisible, 35, 234, 100, 22, 0, a.fontBold); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Source", wsChild|wsVisible, 150, 234, 150, 22, 0, a.fontBold); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Low (4 mA)", wsChild|wsVisible, 330, 234, 150, 22, 0, a.fontBold); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "High (20 mA)", wsChild|wsVisible, 520, 234, 150, 22, 0, a.fontBold); err != nil {
		return err
	}
	if _, err = a.addControl(0, "STATIC", "Log scale", wsChild|wsVisible, 720, 234, 120, 22, 0, a.fontBold); err != nil {
		return err
	}

	for index := 0; index < 2; index++ {
		y := 260 + index*40
		baseID := idSource1
		if index == 1 {
			baseID = idSource2
		}
		if _, err = a.addControl(0, "STATIC", fmt.Sprintf("Analog output %d", index+1), wsChild|wsVisible, 35, y+4, 105, 22, 0, a.font); err != nil {
			return err
		}
		if a.source[index], err = a.addControl(wsExClientEdge, "COMBOBOX", "", wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropdownList|cbsHasStrings, 150, y, 150, 180, baseID, a.font); err != nil {
			return err
		}
		for _, source := range allowedSources {
			ptr := utf16Ptr(source)
			procSendMessageW.Call(a.source[index], cbAddString, 0, uintptr(unsafe.Pointer(ptr)))
		}
		procSendMessageW.Call(a.source[index], cbSetCurSel, uintptr(index*2), 0)
		if a.low[index], err = a.addControl(wsExClientEdge, "EDIT", "0", wsChild|wsVisible|wsTabStop|esLeft|esAutoHScroll, 330, y, 150, 25, baseID+1, a.font); err != nil {
			return err
		}
		high := "100"
		if index == 0 {
			high = "100000"
		}
		if a.high[index], err = a.addControl(wsExClientEdge, "EDIT", high, wsChild|wsVisible|wsTabStop|esLeft|esAutoHScroll, 520, y, 150, 25, baseID+2, a.font); err != nil {
			return err
		}
		if a.log[index], err = a.addControl(0, "BUTTON", "Enabled", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 720, y, 110, 25, baseID+3, a.font); err != nil {
			return err
		}
	}
	if _, err = a.addControl(0, "STATIC", "Values accept a decimal point or comma.", wsChild|wsVisible, 35, 342, 760, 22, 0, a.font); err != nil {
		return err
	}

	if _, err = a.addControl(0, "BUTTON", "Operation log", wsChild|wsVisible|bsGroupBox, 12, 386, 880, 270, 0, a.font); err != nil {
		return err
	}
	if a.clearLog, err = a.addControl(0, "BUTTON", "Clear Log", wsChild|wsVisible|wsTabStop|bsPushButton, 775, 403, 95, 26, idClearLog, a.font); err != nil {
		return err
	}
	if a.logText, err = a.addControl(wsExClientEdge, "EDIT", "", wsChild|wsVisible|wsVScroll|esLeft|esMultiline|esAutoVScroll|esReadOnly, 28, 430, 842, 205, 0, a.font); err != nil {
		return err
	}
	if a.statusText, err = a.addControl(0, "STATIC", "Ready. Enter the instrument address and credentials.", wsChild|wsVisible, 14, 670, 878, 28, 0, a.fontBold); err != nil {
		return err
	}

	a.appendLog("Application started. No credentials will be written to disk.")
	procSetFocus.Call(a.address)
	return nil
}

func (a *application) addControl(exStyle uint32, class, text string, style uint32, x, y, width, height, id int, font uintptr) (uintptr, error) {
	classPtr := utf16Ptr(class)
	textPtr := utf16Ptr(text)
	hwnd, _, callErr := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(width), uintptr(height),
		a.hwnd, uintptr(id), a.hInstance, 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("could not create %s control %d: %v", class, id, callErr)
	}
	if font != 0 {
		procSendMessageW.Call(hwnd, wmSetFont, font, 1)
	}
	return hwnd, nil
}

func (a *application) handleCommand(id int) {
	switch id {
	case idConnect:
		a.startOperation(opConnect)
	case idSendSettings:
		a.startOperation(opSend)
	case idCancel:
		if a.cancel != nil {
			a.setStatus("Cancelling operation...")
			a.appendLog("Cancellation requested.")
			a.cancel()
		}
	case idShowPassword:
		a.updatePasswordVisibility()
	case idInvalidTLS:
		a.invalidateConnection()
	case idClearCredentials:
		a.invalidateConnection()
		setText(a.username, "")
		setText(a.password, "")
		setChecked(a.showPassword, false)
		a.updatePasswordVisibility()
		a.setStatus("Credentials cleared from the form.")
		a.appendLog("Credentials cleared from process memory fields.")
	case idClearLog:
		setText(a.logText, "")
		a.appendLog("Log cleared.")
	}
}

func (a *application) startOperation(kind operationKind) {
	if a.busy {
		return
	}

	cfg, err := a.readConnectionConfig()
	if err != nil {
		a.showValidationError(err)
		return
	}

	var desired []AnalogOutput
	if kind == opSend {
		desired, err = a.readOutputForm()
		if err != nil {
			a.showValidationError(err)
			return
		}
		message := fmt.Sprintf(
			"Send these values to %s?\n\nAnalog output 1\n  Source: %s\n  Low (4 mA): %s\n  High (20 mA): %s\n  Log scale: %t\n\nAnalog output 2\n  Source: %s\n  Low (4 mA): %s\n  High (20 mA): %s\n  Log scale: %t\n\nThe utility will post only the analogOutputs field and then read /api/settings back for verification.",
			cfg.BaseURL.String(),
			desired[0].Source, formatNumber(desired[0].Low), formatNumber(desired[0].High), desired[0].Log,
			desired[1].Source, formatNumber(desired[1].Low), formatNumber(desired[1].High), desired[1].Log,
		)
		if messageBox(a.hwnd, "Confirm Settings Update", message, mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return
		}
	}
	if kind == opConnect {
		a.connected = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.setBusy(true)
	name := operationName(kind)
	a.setStatus(name + " in progress...")
	a.appendLog(fmt.Sprintf("%s started for %s", name, cfg.BaseURL.String()))
	if cfg.BaseURL.Scheme == "http" {
		a.appendLog("Warning: HTTP Basic credentials are being sent over an unencrypted HTTP connection.")
	}
	if cfg.BaseURL.Scheme == "https" && cfg.AllowInvalidTLS {
		a.appendLog("Warning: HTTPS certificate verification is disabled for this operation.")
	}

	go func() {
		result := operationResult{kind: kind}
		client, clientErr := newAPIClient(cfg)
		if clientErr != nil {
			result.err = clientErr
			a.postOperationResult(result)
			return
		}
		defer client.Close()

		switch kind {
		case opConnect:
			result.status, result.err = client.TestConnection(ctx)
			if result.err == nil {
				result.outputs, result.err = client.ReadAnalogOutputs(ctx)
			}
		case opSend:
			result.outputs, result.err = client.UpdateAnalogOutputs(ctx, desired)
		}
		a.postOperationResult(result)
	}()
}

func (a *application) postOperationResult(result operationResult) {
	a.resultMu.Lock()
	a.results = append(a.results, result)
	a.resultMu.Unlock()
	if !a.closing.Load() {
		procPostMessageW.Call(a.hwnd, wmAppResult, 0, 0)
	}
}

func (a *application) handleOperationResults() {
	a.resultMu.Lock()
	results := append([]operationResult(nil), a.results...)
	a.results = a.results[:0]
	a.resultMu.Unlock()

	for _, result := range results {
		a.cancel = nil
		clearPassword := !isChecked(a.keepPassword) && getText(a.password) != ""
		if result.err != nil && clearPassword {
			setText(a.password, "")
		}

		if result.err != nil {
			a.setBusy(false)
			if errors.Is(result.err, context.Canceled) {
				a.setStatus("Operation cancelled.")
				a.appendLog(operationName(result.kind) + " cancelled.")
				continue
			}
			friendly := friendlyError(result.err)
			a.setStatus("Operation failed. See the log for details.")
			a.appendLog(operationName(result.kind) + " failed: " + friendly)
			messageBox(a.hwnd, operationName(result.kind)+" Failed", friendly, mbOK|mbIconError)
			continue
		}

		switch result.kind {
		case opConnect:
			a.populateOutputs(result.outputs)
			a.connected = true
			message := fmt.Sprintf("Connected successfully. The instrument returned HTTP %d and its settings were loaded.", result.status)
			a.setStatus(message)
			a.appendLog(message)
		case opSend:
			message := "Settings sent and verified successfully."
			a.setStatus(message)
			a.appendLog(message)
			messageBox(a.hwnd, "Settings Updated", message, mbOK|mbIconInfo)
		}
		a.setBusy(false)
		if clearPassword {
			setText(a.password, "")
			a.connected = false
			procEnableWindow.Call(a.sendButton, 0)
			a.setStatus("Password cleared. Enter it and Connect again before sending settings.")
			a.appendLog("Password cleared because Keep password until app closes is disabled.")
		}
	}
}

func (a *application) readConnectionConfig() (ConnectionConfig, error) {
	baseURL, err := normalizeBaseURL(getText(a.address))
	if err != nil {
		return ConnectionConfig{}, &fieldError{message: err.Error(), control: a.address}
	}
	username := strings.TrimSpace(getText(a.username))
	if username == "" {
		return ConnectionConfig{}, &fieldError{message: "username is required", control: a.username}
	}
	if strings.Contains(username, ":") {
		return ConnectionConfig{}, &fieldError{message: "username must not contain ':' when using HTTP Basic authentication", control: a.username}
	}
	timeoutText := strings.TrimSpace(getText(a.timeout))
	timeoutSeconds, err := strconv.Atoi(timeoutText)
	if err != nil || timeoutSeconds < 1 || timeoutSeconds > 300 {
		return ConnectionConfig{}, &fieldError{message: "timeout must be a whole number from 1 to 300 seconds", control: a.timeout}
	}
	return ConnectionConfig{
		BaseURL:         baseURL,
		Username:        username,
		Password:        getText(a.password),
		Timeout:         time.Duration(timeoutSeconds) * time.Second,
		AllowInvalidTLS: isChecked(a.invalidTLS),
	}, nil
}

func (a *application) readOutputForm() ([]AnalogOutput, error) {
	outputs := make([]AnalogOutput, 2)
	for index := 0; index < 2; index++ {
		selection, _, _ := procSendMessageW.Call(a.source[index], cbGetCurSel, 0, 0)
		if int32(selection) < 0 || int(selection) >= len(allowedSources) {
			return nil, &fieldError{message: fmt.Sprintf("select a source for analog output %d", index+1), control: a.source[index]}
		}
		low, err := parseNumber(getText(a.low[index]))
		if err != nil {
			return nil, &fieldError{message: fmt.Sprintf("analog output %d low value: %v", index+1, err), control: a.low[index]}
		}
		high, err := parseNumber(getText(a.high[index]))
		if err != nil {
			return nil, &fieldError{message: fmt.Sprintf("analog output %d high value: %v", index+1, err), control: a.high[index]}
		}
		outputs[index] = AnalogOutput{
			Source: allowedSources[int(selection)],
			Low:    low,
			High:   high,
			Log:    isChecked(a.log[index]),
		}
	}
	return outputs, nil
}

func (a *application) populateOutputs(outputs []AnalogOutput) {
	if len(outputs) != 2 {
		return
	}
	for index, output := range outputs {
		for sourceIndex, source := range allowedSources {
			if source == output.Source {
				procSendMessageW.Call(a.source[index], cbSetCurSel, uintptr(sourceIndex), 0)
				break
			}
		}
		setText(a.low[index], formatNumber(output.Low))
		setText(a.high[index], formatNumber(output.High))
		setChecked(a.log[index], output.Log)
	}
}

func (a *application) showValidationError(err error) {
	var field *fieldError
	if errors.As(err, &field) && field.control != 0 {
		procSetFocus.Call(field.control)
	}
	a.setStatus("Please correct the form.")
	messageBox(a.hwnd, "Invalid Input", err.Error(), mbOK|mbIconWarning)
}

func (a *application) setBusy(busy bool) {
	a.busy = busy
	interactive := []uintptr{
		a.address, a.username, a.password, a.timeout, a.showPassword, a.keepPassword, a.invalidTLS,
		a.connectButton, a.clearCredentials,
		a.source[0], a.source[1], a.low[0], a.low[1], a.high[0], a.high[1], a.log[0], a.log[1],
	}
	for _, control := range interactive {
		if control != 0 {
			enabled := uintptr(1)
			if busy {
				enabled = 0
			}
			procEnableWindow.Call(control, enabled)
		}
	}
	if a.sendButton != 0 {
		enabled := uintptr(0)
		if !busy && a.connected {
			enabled = 1
		}
		procEnableWindow.Call(a.sendButton, enabled)
	}
	if a.cancelButton != 0 {
		enabled := uintptr(0)
		if busy {
			enabled = 1
		}
		procEnableWindow.Call(a.cancelButton, enabled)
	}
}

func (a *application) invalidateConnection() {
	if !a.connected {
		return
	}
	a.connected = false
	if a.sendButton != 0 {
		procEnableWindow.Call(a.sendButton, 0)
	}
	a.setStatus("Connection details changed. Connect again before sending settings.")
}

func (a *application) updatePasswordVisibility() {
	character := uintptr('●')
	if isChecked(a.showPassword) {
		character = 0
	}
	procSendMessageW.Call(a.password, emSetPasswordChar, character, 0)
	procInvalidateRect.Call(a.password, 0, 1)
}

func (a *application) setStatus(text string) {
	setText(a.statusText, text)
}

func (a *application) appendLog(text string) {
	if a.logText == 0 {
		return
	}
	length, _, _ := procGetWindowTextLengthW.Call(a.logText)
	if length > 50000 {
		setText(a.logText, "")
	}
	line := fmt.Sprintf("[%s] %s\r\n", time.Now().Format("15:04:05"), text)
	ptr := utf16Ptr(line)
	procSendMessageW.Call(a.logText, emSetSel, ^uintptr(0), ^uintptr(0))
	procSendMessageW.Call(a.logText, emReplaceSel, 0, uintptr(unsafe.Pointer(ptr)))
	procSendMessageW.Call(a.logText, emScrollCaret, 0, 0)
}

func operationName(kind operationKind) string {
	switch kind {
	case opConnect:
		return "Connect"
	case opSend:
		return "Settings update"
	default:
		return "Operation"
	}
}

func createUIFont(weight int) (uintptr, bool) {
	face := utf16Ptr("Segoe UI")
	height := int32(-13)
	font, _, _ := procCreateFontW.Call(
		uintptr(uint32(height)), 0, 0, 0, uintptr(weight),
		0, 0, 0, defaultCharset, 0, 0, clearTypeQuality, 0,
		uintptr(unsafe.Pointer(face)),
	)
	if font != 0 {
		return font, true
	}
	stock, _, _ := procGetStockObject.Call(17) // DEFAULT_GUI_FONT
	return stock, false
}

func getText(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	buffer := make([]uint16, int(length)+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return syscall.UTF16ToString(buffer)
}

func setText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	ptr := utf16Ptr(text)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(ptr)))
}

func isChecked(hwnd uintptr) bool {
	result, _, _ := procSendMessageW.Call(hwnd, bmGetCheck, 0, 0)
	return result == bstChecked
}

func setChecked(hwnd uintptr, checked bool) {
	value := uintptr(bstUnchecked)
	if checked {
		value = bstChecked
	}
	procSendMessageW.Call(hwnd, bmSetCheck, value, 0)
}

func messageBox(owner uintptr, title, text string, flags uintptr) int {
	result, _, _ := procMessageBoxW.Call(
		owner,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		flags,
	)
	return int(result)
}

func utf16Ptr(text string) *uint16 {
	text = strings.ReplaceAll(text, "\x00", "\uFFFD")
	ptr, _ := syscall.UTF16PtrFromString(text)
	return ptr
}
