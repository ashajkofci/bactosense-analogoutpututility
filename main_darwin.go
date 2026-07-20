//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type macOperationRequest struct {
	Operation       string         `json:"operation"`
	Address         string         `json:"address"`
	Username        string         `json:"username"`
	Password        string         `json:"password"`
	TimeoutSeconds  int            `json:"timeoutSeconds"`
	AllowInvalidTLS bool           `json:"allowInvalidTLS"`
	Outputs         []AnalogOutput `json:"outputs"`
}

type macOperationResponse struct {
	Status  int            `json:"status,omitempty"`
	Outputs []AnalogOutput `json:"outputs,omitempty"`
	Message string         `json:"message,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func main() {
	if err := runMacApp(); err != nil {
		fmt.Fprintln(os.Stderr, "Analog Output Utility:", err)
	}
}

func runMacApp() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes[:])
	quit := make(chan struct{})
	var quitOnce sync.Once
	server := &http.Server{
		Handler:           newMacHandler(token, func() { quitOnce.Do(func() { close(quit) }) }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	appURL := "http://" + listener.Addr().String() + "/" + token + "/"
	if err := exec.Command("open", appURL).Start(); err != nil {
		_ = server.Close()
		return fmt.Errorf("could not open the browser: %w", err)
	}

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-quit:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}

func newMacHandler(token string, quit func()) http.Handler {
	prefix := "/" + token
	busy := make(chan struct{}, 1)
	mux := http.NewServeMux()

	mux.HandleFunc(prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != prefix+"/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, strings.Replace(macPage, "{{VERSION}}", html.EscapeString(version), 1))
	})

	mux.HandleFunc(prefix+"/operation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeMacJSON(w, http.StatusMethodNotAllowed, macOperationResponse{Error: "POST required"})
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			writeMacJSON(w, http.StatusConflict, macOperationResponse{Error: "Another operation is already running."})
			return
		}

		var input macOperationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: "Invalid request: " + err.Error()})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: "Invalid request: expected one JSON object"})
			return
		}

		baseURL, err := normalizeBaseURL(input.Address)
		if err != nil {
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: err.Error()})
			return
		}
		username := strings.TrimSpace(input.Username)
		if input.TimeoutSeconds < 1 || input.TimeoutSeconds > 300 {
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: "timeout must be a whole number from 1 to 300 seconds"})
			return
		}
		if input.Operation == "send" {
			if err := validateOutputs(input.Outputs); err != nil {
				writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: err.Error()})
				return
			}
		}
		client, err := newAPIClient(ConnectionConfig{
			BaseURL:         baseURL,
			Username:        username,
			Password:        input.Password,
			Timeout:         time.Duration(input.TimeoutSeconds) * time.Second,
			AllowInvalidTLS: input.AllowInvalidTLS,
		})
		if err != nil {
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: err.Error()})
			return
		}
		defer client.Close()

		response := macOperationResponse{}
		switch input.Operation {
		case "test":
			response.Status, err = client.TestConnection(r.Context())
			response.Message = "Connection successful. The device returned HTTP " + strconv.Itoa(response.Status) + " from /api/status."
		case "read":
			response.Outputs, err = client.ReadAnalogOutputs(r.Context())
			response.Message = "Settings read successfully. The form now contains both analog outputs."
		case "send":
			response.Outputs, err = client.UpdateAnalogOutputs(r.Context(), input.Outputs)
			response.Message = "Settings sent and verified successfully."
		default:
			writeMacJSON(w, http.StatusBadRequest, macOperationResponse{Error: "operation must be test, read, or send"})
			return
		}
		if err != nil {
			writeMacJSON(w, http.StatusBadGateway, macOperationResponse{Error: friendlyError(err)})
			return
		}
		writeMacJSON(w, http.StatusOK, response)
	})

	mux.HandleFunc(prefix+"/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeMacJSON(w, http.StatusMethodNotAllowed, macOperationResponse{Error: "POST required"})
			return
		}
		writeMacJSON(w, http.StatusOK, macOperationResponse{Message: "Analog Output Utility has stopped."})
		quit()
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func writeMacJSON(w http.ResponseWriter, status int, value macOperationResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const macPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Analog Output Utility</title>
<style>
:root{color-scheme:light dark;font:15px -apple-system,BlinkMacSystemFont,"Helvetica Neue",sans-serif}body{margin:0;background:#f3f4f7;color:#202124}main{max-width:900px;margin:24px auto;padding:0 18px}h1{font-size:25px;margin:0 0 18px}small,.hint{color:#666}fieldset{border:1px solid #c9ccd3;border-radius:10px;background:#fff;margin:0 0 14px;padding:16px}legend{font-weight:600;padding:0 7px}.connection{display:grid;grid-template-columns:130px 1fr 90px 90px;gap:10px;align-items:center}.outputs{display:grid;grid-template-columns:150px 1fr 1fr 1fr 100px;gap:10px;align-items:center}.head{font-weight:600}input,select,button{font:inherit}input,select{box-sizing:border-box;width:100%;padding:7px 9px;border:1px solid #aeb2ba;border-radius:6px;background:#fff;color:#202124}input[type=checkbox]{width:auto}.check{display:flex;gap:7px;align-items:center}.wide{grid-column:2/-1}.actions{display:flex;gap:9px;flex-wrap:wrap;margin-top:13px}button{border:1px solid #999fa8;border-radius:7px;padding:7px 13px;background:#fff;color:#202124;cursor:pointer}button.primary{background:#1769e0;border-color:#1769e0;color:#fff}button.danger{margin-left:auto}button:disabled{opacity:.5;cursor:default}#log{box-sizing:border-box;width:100%;height:180px;resize:vertical;padding:10px;border:1px solid #aeb2ba;border-radius:7px;background:#fafafa;color:#202124;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}#status{font-weight:600;margin:12px 2px 0}@media(max-width:700px){.connection,.outputs{grid-template-columns:1fr 1fr}.connection label,.outputs .head{display:none}.wide{grid-column:1/-1}.output-name{grid-column:1/-1;font-weight:600}.danger{margin-left:0!important}}@media(prefers-color-scheme:dark){body{background:#1d1e20;color:#eee}fieldset{background:#292b2f;border-color:#4a4d53}small,.hint{color:#aaa}input,select,button{background:#34363b;color:#eee;border-color:#62666e}button.primary{background:#287be8}#log{background:#202124;color:#eee;border-color:#62666e}}
</style>
</head>
<body><main>
<h1>Analog Output Utility <small>{{VERSION}}</small></h1>
<form id="form" autocomplete="off">
<fieldset><legend>Device connection</legend><div class="connection">
<label for="address">Device address</label><input id="address" class="wide" required placeholder="192.168.1.50 or https://device.local/api">
<label for="username">Username</label><input id="username" required autocomplete="off"><label for="timeout">Timeout (seconds)</label><input id="timeout" type="number" min="1" max="300" step="1" value="10" required>
<label for="password">Password</label><input id="password" type="password" autocomplete="new-password"><label class="check"><input id="showPassword" type="checkbox"> Show</label><span></span>
<span></span><label class="check wide"><input id="keepPassword" type="checkbox" checked> Keep password until app closes</label>
<span></span><label class="check wide"><input id="invalidTLS" type="checkbox" checked> Allow an invalid HTTPS certificate</label>
</div><p class="hint">Credentials stay in this process and browser tab only. HTTP Basic authentication over plain HTTP is not encrypted.</p>
<div class="actions"><button type="button" class="primary" data-operation="test">Test Connection</button><button type="button" data-operation="read">Read Settings</button><button type="button" data-operation="send">Send Settings</button><button type="button" id="cancel" disabled>Cancel</button><button type="button" id="clearCredentials">Clear Credentials</button></div>
</fieldset>
<fieldset><legend>Analog outputs</legend><div class="outputs">
<span class="head">Output</span><span class="head">Source</span><span class="head">Low (4 mA)</span><span class="head">High (20 mA)</span><span class="head">Log scale</span>
<span class="output-name">Analog output 1</span><select id="source1"><option>TCC</option><option>ICC</option><option>HNAP</option><option>HNAC</option><option>LNAC</option></select><input id="low1" value="0" required><input id="high1" value="100000" required><label class="check"><input id="log1" type="checkbox"> Enabled</label>
<span class="output-name">Analog output 2</span><select id="source2"><option>HNAP</option><option>TCC</option><option>ICC</option><option>HNAC</option><option>LNAC</option></select><input id="low2" value="0" required><input id="high2" value="100" required><label class="check"><input id="log2" type="checkbox"> Enabled</label>
</div><p class="hint">Values accept a decimal point or comma.</p></fieldset>
<fieldset><legend>Operation log</legend><textarea id="log" readonly></textarea><div class="actions"><button type="button" id="clearLog">Clear Log</button><button type="button" class="danger" id="quit">Quit Utility</button></div></fieldset>
<div id="status" role="status" aria-live="polite">Ready. Enter the device address and credentials.</div>
</form></main>
<script>
const $=id=>document.getElementById(id),form=$('form'),logBox=$('log'),statusBox=$('status');let controller;
function log(text){const now=new Date().toLocaleTimeString([], {hour12:false});logBox.value+='['+now+'] '+text+'\n';logBox.scrollTop=logBox.scrollHeight}
function number(id){const text=$(id).value.trim().replace(',','.');const value=Number(text);if(text===''||!Number.isFinite(value))throw new Error(id+' must be a finite number');return value}
function outputs(){return[1,2].map(i=>({source:$('source'+i).value,low:number('low'+i),high:number('high'+i),log:$('log'+i).checked}))}
function fill(values){if(!values||values.length!==2)return;values.forEach((o,n)=>{const i=n+1;$('source'+i).value=o.source;$('low'+i).value=o.low;$('high'+i).value=o.high;$('log'+i).checked=o.log})}
function setBusy(busy){form.querySelectorAll('input,select,button[data-operation],#clearCredentials').forEach(e=>e.disabled=busy);$('cancel').disabled=!busy}
async function operate(operation){if(!$('address').reportValidity()||!$('username').reportValidity()||!$('timeout').reportValidity())return;let values=[];if(operation==='send'){try{values=outputs()}catch(e){statusBox.textContent=e.message;return}if(!confirm('Send both analog outputs to '+$('address').value+'? The utility will post only analogOutputs and read the settings back for verification.'))return}
const request={operation,address:$('address').value,username:$('username').value,password:$('password').value,timeoutSeconds:Number($('timeout').value),allowInvalidTLS:$('invalidTLS').checked,outputs:values};controller=new AbortController;setBusy(true);statusBox.textContent='Operation in progress…';log(operation+' started for '+request.address);if(!/^https:\/\//i.test(request.address))log('Warning: HTTP Basic credentials may be sent over an unencrypted connection.');if(request.allowInvalidTLS)log('Warning: HTTPS certificate verification is disabled for this operation.');
try{const response=await fetch('operation',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(request),signal:controller.signal});const data=await response.json();if(!response.ok)throw new Error(data.error||'Request failed (HTTP '+response.status+')');fill(data.outputs);statusBox.textContent=data.message;log(data.message)}catch(e){if(e.name==='AbortError'){statusBox.textContent='Operation cancelled.';log('Cancellation requested.')}else{statusBox.textContent='Operation failed. See the log for details.';log(e.message);alert(e.message)}}finally{if(!$('keepPassword').checked)$('password').value='';controller=undefined;setBusy(false)}}
document.querySelectorAll('[data-operation]').forEach(button=>button.onclick=()=>operate(button.dataset.operation));$('cancel').onclick=()=>controller?.abort();$('showPassword').onchange=()=>$('password').type=$('showPassword').checked?'text':'password';$('clearCredentials').onclick=()=>{$('username').value='';$('password').value='';statusBox.textContent='Credentials cleared from the form.';log('Credentials cleared from process memory fields.')};$('clearLog').onclick=()=>{logBox.value='';log('Log cleared.')};$('quit').onclick=async()=>{if(controller)controller.abort();await fetch('quit',{method:'POST'});document.body.innerHTML='<main><h1>Analog Output Utility has stopped.</h1><p>You can close this tab.</p></main>'};log('Application started. No credentials will be written to disk.');$('address').focus();
</script></body></html>`
