package extension

import (
	"billing3/database"
	"billing3/database/types"
	"billing3/utils"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	_ "embed"
)

var (

	//go:embed pveinfo.html
	pveInfoHtml string

	//go:embed pvevnc.html
	pveVncHtml string

	pveWebsocketUpgrader = websocket.Upgrader{
		HandshakeTimeout: time.Minute,
	}

	pveWebsocketDialer = websocket.Dialer{
		HandshakeTimeout: time.Minute,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
)

var errNoServerAssigned = errors.New("no server assigned")

type PVE struct {
	httpClient http.Client
	infoPage   *template.Template
	vncPage    *template.Template
}

type pveResp[T any] struct {
	Data T `json:"data"`
}

type pveVmInfo struct {
	Status      string
	MaxDisk     int
	MaxMemory   int
	Name        string
	Cores       int
	IPv4        string
	IPv4Gateway string
	Username    string
	Password    string
	OS          [][]string
}

// pveAuth returns CSRFPreventionToken and ticket
func (p *PVE) pveAuth(base string, username string, password string) (string, string, error) {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)

	httpResp, err := p.httpClient.Post(base+"/access/ticket", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("auth: %w", err)
	}
	defer httpResp.Body.Close()

	all, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", "", fmt.Errorf("auth: %w", err)
	}

	slog.Debug("pve auth", "base", base, "username", username, "resp", string(all), "status", httpResp.Status)

	if httpResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("auth: %s", httpResp.Status)
	}

	resp := pveResp[struct {
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Ticket              string `json:"ticket"`
		Username            string `json:"username"`
	}]{}
	err = json.Unmarshal(all, &resp)
	if err != nil {
		return "", "", fmt.Errorf("auth: %w", err)
	}

	return resp.Data.CSRFPreventionToken, resp.Data.Ticket, nil
}

func (p *PVE) apiGet(api string, resp any, ticket string) error {
	req, err := http.NewRequest("GET", api, nil)
	if err != nil {
		return fmt.Errorf("api get: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("api get: %w", err)
	}
	defer httpResp.Body.Close()

	all, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("api get: %w", err)
	}

	slog.Debug("pve get", "url", api, "resp", string(all), "status", httpResp.Status)

	if httpResp.StatusCode/100 != 2 {
		return fmt.Errorf("api post: Status code: %s", httpResp.Status)
	}

	err = json.Unmarshal(all, &resp)
	if err != nil {
		return fmt.Errorf("api get: %w", err)
	}
	return nil
}

func (p *PVE) apiAction(method string, api string, body url.Values, resp any, csrf string, ticket string) error {
	req, err := http.NewRequest(method, api, strings.NewReader(body.Encode()))
	if err != nil {
		return fmt.Errorf("api post: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})
	req.Header.Set("CSRFPreventionToken", csrf)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("api post: %w", err)
	}
	defer httpResp.Body.Close()

	all, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("api post: %w", err)
	}

	slog.Debug("pve post", "url", api, "body", body, "resp", string(all), "status", httpResp.Status)

	if httpResp.StatusCode/100 != 2 {
		return fmt.Errorf("api post: status code: %s", httpResp.Status)
	}

	err = json.Unmarshal(all, resp)
	if err != nil {
		return fmt.Errorf("api post: %w", err)
	}

	return nil
}

func (p *PVE) createService(serviceId int32) error {
	ctx := context.Background()

	s, err := database.Q.FindServiceById(ctx, serviceId)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	cpu := s.Settings["cpu"]
	disk := s.Settings["disk"]
	memory := s.Settings["memory"]
	servers := s.Settings["servers"]
	vmType := s.Settings["vm_type"]
	vmPassword := s.Settings["vm_password"]
	kvmTemplateVmid, _ := s.Settings["kvm_template_vmid"]
	lxcTemplate, _ := s.Settings["lxc_template"]

	serverIds := make([]int, 0)
	for _, s := range strings.Split(servers, ",") {
		i, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("pve: invalid servers: %s", servers)
		}
		serverIds = append(serverIds, i)
	}

	if len(serverIds) == 0 {
		return fmt.Errorf("pve: no servers available")
	}

	// choose a random pve server
	serverId, _ := utils.RandomChoose(serverIds)

	// pve server settings
	server, err := database.Q.FindServerById(ctx, int32(serverId))
	if err != nil {
		return fmt.Errorf("pve: invalid servers: %d %w", serverId, err)
	}

	address := server.Settings["address"]
	port := server.Settings["port"]
	username := server.Settings["username"]
	password := server.Settings["password"]
	node := server.Settings["node"]
	ips := server.Settings["ips"]
	gateway := server.Settings["gateway"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)
	bridge := server.Settings["bridge"]

	// choose an IP address
	var ip string
	var ok bool

	ip, ok = s.Settings["ip"]
	if !ok {

		// ip address is not assigned yet, choose one from server settings

		unusedIps := utils.Filter(strings.Split(ips, "\n"), func(ip string) bool {
			// ip with # prefix are currently in use
			return len(ip) != 0 && ip[0] != '#'
		})
		if len(unusedIps) == 0 {
			return fmt.Errorf("pve: server %d: no unused ips available", serverId)
		}
		ip, _ = utils.RandomChoose(unusedIps)

	}

	slog.Info("pve create", "server id", serverId, "servers", servers, "cpu", cpu, "disk", disk, "memory", memory, "pve base", baseUrl, "node", node, "vm type", vmType, "kvm template vmid", kvmTemplateVmid, "ip", ip)

	// pve auth
	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	// vmid
	vmid := int(10000 + serviceId)

	switch vmType {
	case "kvm":

		// kvm clone

		if kvmTemplateVmid == "" {
			return fmt.Errorf("pve: kvm_template_vmid is required for kvm vm_type")
		}

		resp := pveResp[string]{}
		form := url.Values{}
		form.Set("newid", strconv.Itoa(vmid))
		form.Set("full", "1")
		form.Set("name", fmt.Sprintf("service%d", serviceId))
		err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/qemu/%s/clone", baseUrl, node, kvmTemplateVmid), form, &resp, csrf, ticket)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		err = p.waitForTask(baseUrl, node, ticket, resp.Data)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		// vm config
		resp = pveResp[string]{}
		form = url.Values{}
		form.Set("cipassword", vmPassword)
		form.Set("ciuser", "vmuser")
		form.Set("cores", cpu)
		form.Set("memory", memory)
		form.Set("ipconfig0", fmt.Sprintf("gw=%s,ip=%s", gateway, ip))
		form.Set("nameserver", "8.8.8.8")
		form.Set("searchdomain", ".")
		form.Set("boot", "order=scsi0")
		err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/qemu/%d/config", baseUrl, node, vmid), form, &resp, csrf, ticket)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		err = p.waitForTask(baseUrl, node, ticket, resp.Data)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		// resize disk
		resp = pveResp[string]{}
		form = url.Values{}
		form.Set("disk", "scsi0")
		form.Set("size", disk+"G")
		err = p.apiAction("PUT", fmt.Sprintf("%s/nodes/%s/qemu/%d/resize", baseUrl, node, vmid), form, &resp, csrf, ticket)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		err = p.waitForTask(baseUrl, node, ticket, resp.Data)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

	case "lxc":

		// create lxc

		if lxcTemplate == "" {
			return fmt.Errorf("pve: lxc_template is required for lxc vm_type")
		}

		form := url.Values{}
		form.Set("vmid", strconv.Itoa(vmid))
		form.Set("unprivileged", "1")
		form.Set("features", "nesting=1")
		form.Set("password", vmPassword)
		form.Set("ssh-public-keys", "")
		form.Set("ostemplate", lxcTemplate)
		form.Set("rootfs", fmt.Sprintf("local:%s", disk))
		form.Set("cores", cpu)
		form.Set("memory", memory)
		form.Set("swap", "0")
		form.Set("net0", fmt.Sprintf("name=eth0,bridge=%s,firewall=1,ip=%s,gw=%s", bridge, ip, gateway))
		form.Set("nameserver", "8.8.8.8")

		lxcResp := pveResp[string]{}

		err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/lxc", baseUrl, node), form, &lxcResp, csrf, ticket)
		if err != nil {
			return fmt.Errorf("pve: lxc create: %w", err)
		}

		err = p.waitForTask(baseUrl, node, ticket, lxcResp.Data)
		if err != nil {
			return fmt.Errorf("pve: wait lxc create: %w", err)
		}

	default:
		return fmt.Errorf("bad vm_type: %s", vmType)
	}

	// save server id and ip address
	s.Settings["server"] = strconv.Itoa(serverId)
	s.Settings["ip"] = ip
	err = database.Q.UpdateServiceSettings(ctx, database.UpdateServiceSettingsParams{
		ID:       int32(serviceId),
		Settings: s.Settings,
	})
	if err != nil {
		return fmt.Errorf("pve: db: %w", err)
	}

	// mark the ip ad used
	newIps := ""
	for _, p := range strings.Split(ips, "\n") {
		if len(p) == 0 {
			continue
		}
		if p == ip {
			newIps += "#" + ip + "\n"
		} else {
			newIps += p + "\n"
		}
	}
	server.Settings["ips"] = newIps
	err = database.Q.UpdateServerSettings(ctx, database.UpdateServerSettingsParams{
		ID:       int32(serverId),
		Settings: server.Settings,
	})
	if err != nil {
		return fmt.Errorf("pve: db: %w", err)
	}

	return nil
}

// waitForTask waits for the task to finish. Timeout if task is not finished within 50 seconds.
// waitForTask returns non-nil error if the task fails or timeouts.
func (p *PVE) waitForTask(baseUrl string, node string, ticket string, taskId string) error {
	slog.Debug("pve wait for task", "task id", taskId, "base url", baseUrl, "node", node)

	for range 10 {
		resp := pveResp[struct {
			Status     string  `json:"status"`
			Id         string  `json:"id"`
			ExitStatus *string `json:"exitstatus"`
			Type       string  `json:"type"`
		}]{}

		err := p.apiGet(fmt.Sprintf("%s/nodes/%s/tasks/%s/status", baseUrl, node, taskId), &resp, ticket)
		if err != nil {
			return fmt.Errorf("wait for task: %w", err)
		}

		slog.Debug("pve wait for task", "task id", taskId, "base url", baseUrl, "node", node, "type", resp.Data.Type, "status", resp.Data.Status, "exit status", resp.Data.ExitStatus)

		if resp.Data.Status == "stopped" {
			// https://github.com/proxmox/pve-common/blob/ad169fbd08343a86e43275ef93f94a4d00d44932/src/PVE/Tools.pm#L1256
			if resp.Data.ExitStatus == nil || *resp.Data.ExitStatus == "OK" || strings.HasPrefix(*resp.Data.ExitStatus, "WARNING") {
				return nil
			}
			return fmt.Errorf("task %s failed: %s", taskId, *resp.Data.ExitStatus)
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("task timeout: %s", taskId)
}

// getServiceSettings returns the service and server settings for the service id.
func (p *PVE) getServiceSettings(serviceId int32) (types.ServiceSettings, types.ServerSettings, error) {
	s, err := database.Q.FindServiceById(context.Background(), serviceId)
	if err != nil {
		return nil, nil, fmt.Errorf("get service settings: db: %w", err)
	}
	serverIdStr, ok := s.Settings["server"]
	if !ok {
		return nil, nil, errNoServerAssigned
	}
	serverId, err := strconv.Atoi(serverIdStr)
	if err != nil {
		return nil, nil, fmt.Errorf("get service settings: not integer: %s", serverIdStr)
	}
	ss, err := database.Q.FindServerById(context.Background(), int32(serverId))
	if err != nil {
		return nil, nil, fmt.Errorf("get service settings: db: %w", err)
	}
	return s.Settings, ss.Settings, nil
}

func (p *PVE) qemuPoweroff(serviceId int32, force bool, lxc bool) error {
	_, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return fmt.Errorf("pve: poweroff: %w", err)
	}

	vmType := "qemu"
	if lxc {
		vmType = "lxc"
	}

	address := serverSettings["address"]
	port := serverSettings["port"]
	username := serverSettings["username"]
	password := serverSettings["password"]
	node := serverSettings["node"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	vmid := int(10000 + serviceId)

	body := url.Values{}
	if force {
		body.Set("forceStop", "1")
	} else {
		body.Set("forceStop", "0")
	}
	if !lxc {
		body.Set("timeout", "30")
	}

	resp := pveResp[string]{}
	err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/%s/%d/status/shutdown", baseUrl, node, vmType, vmid), body, &resp, csrf, ticket)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	err = p.waitForTask(baseUrl, node, ticket, resp.Data)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	return nil
}

func (p *PVE) qemuStart(serviceId int32, lxc bool) error {
	_, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return fmt.Errorf("pve: poweroff: %w", err)
	}

	vmType := "qemu"
	if lxc {
		vmType = "lxc"
	}

	address := serverSettings["address"]
	port := serverSettings["port"]
	username := serverSettings["username"]
	password := serverSettings["password"]
	node := serverSettings["node"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	vmid := int(10000 + serviceId)

	body := url.Values{}
	if !lxc {
		body.Set("timeout", "30")
	}

	resp := pveResp[string]{}
	err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/%s/%d/status/start", baseUrl, node, vmType, vmid), body, &resp, csrf, ticket)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	err = p.waitForTask(baseUrl, node, ticket, resp.Data)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	return nil
}

func (p *PVE) qemuReboot(serviceId int32, lxc bool) error {
	_, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return fmt.Errorf("pve: poweroff: %w", err)
	}

	vmType := "qemu"
	if lxc {
		vmType = "lxc"
	}

	address := serverSettings["address"]
	port := serverSettings["port"]
	username := serverSettings["username"]
	password := serverSettings["password"]
	node := serverSettings["node"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	vmid := int(10000 + serviceId)

	body := url.Values{}
	body.Set("timeout", "30")

	resp := pveResp[string]{}
	err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/%s/%d/status/reboot", baseUrl, node, vmType, vmid), body, &resp, csrf, ticket)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	err = p.waitForTask(baseUrl, node, ticket, resp.Data)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	return nil
}

// Delete the VM, unassign the server from the service and mark the IP as unused.
func (p *PVE) qemuDelete(serviceId int32, lxc bool) error {
	serviceSettings, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return fmt.Errorf("pve: poweroff: %w", err)
	}

	vmType := "qemu"
	if lxc {
		vmType = "lxc"
	}

	// delete vm from pve

	address := serverSettings["address"]
	port := serverSettings["port"]
	username := serverSettings["username"]
	password := serverSettings["password"]
	node := serverSettings["node"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	vmid := int(10000 + serviceId)

	resp := pveResp[string]{}
	err = p.apiAction("DELETE", fmt.Sprintf("%s/nodes/%s/%s/%d", baseUrl, node, vmType, vmid), url.Values{}, &resp, csrf, ticket)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	err = p.waitForTask(baseUrl, node, ticket, resp.Data)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	serverIdStr, ok := serviceSettings["server"]
	if !ok {
		return fmt.Errorf("pve: delete: server id not found in service settings")
	}
	serverId, err := strconv.Atoi(serverIdStr)
	if err != nil {
		return fmt.Errorf("pve: delete: bad server id: %s", serverIdStr)
	}

	// unassign server
	delete(serviceSettings, "server")
	err = database.Q.UpdateServiceSettings(context.Background(), database.UpdateServiceSettingsParams{
		ID:       int32(serviceId),
		Settings: serviceSettings,
	})
	if err != nil {
		return fmt.Errorf("pve: unassign server id: %w", err)
	}

	// mark ip as unused
	ip, ok := serviceSettings["ip"]
	if !ok {
		return fmt.Errorf("pve: delete: ip not found in service settings")
	}
	ips, ok := serverSettings["ips"]
	if !ok {
		return fmt.Errorf("pve: delete: ips not found in server settings")
	}

	newIps := ""
	for _, p := range strings.Split(ips, "\n") {
		if len(p) == 0 {
			continue
		}
		if p == "#"+ip {
			newIps += ip + "\n"
		} else {
			newIps += p + "\n"
		}
	}
	serverSettings["ips"] = newIps
	err = database.Q.UpdateServerSettings(context.Background(), database.UpdateServerSettingsParams{
		ID:       int32(serverId),
		Settings: serverSettings,
	})
	if err != nil {
		return fmt.Errorf("pve: mark ip unused: %w", err)
	}

	return nil
}

func (p *PVE) Action(serviceId int32, action string) error {

	serviceSettings, _, err := p.getServiceSettings(serviceId)
	if err != nil && !(errors.Is(err, errNoServerAssigned) && action == "create") {
		return fmt.Errorf("pve: perform action: get service settings: %w", err)
	}

	vmType := "qemu"
	if _, ok := serviceSettings["vm_type"]; ok && vmType == "lxc" {
		vmType = "lxc"
	}

	slog.Info("pve action", "service id", serviceId, "action", action, "vm type", vmType)

	switch action {
	case "reinstall":
		err = p.qemuPoweroff(serviceId, true, vmType == "lxc")
		if err != nil && !strings.Contains(err.Error(), "not running") {
			// ignore error caused by VM not running
			return fmt.Errorf("reinstall: force poweroff: %w", err)
		}
		err = p.qemuDelete(serviceId, vmType == "lxc")
		if err != nil {
			return fmt.Errorf("reinstall: delete: %w", err)
		}
		return p.createService(serviceId)
	case "poweroff":
		return p.qemuPoweroff(serviceId, false, vmType == "lxc")
	case "force_poweroff":
		return p.qemuPoweroff(serviceId, true, vmType == "lxc")
	case "reboot":
		return p.qemuReboot(serviceId, vmType == "lxc")
	case "suspend":
		return p.qemuPoweroff(serviceId, true, vmType == "lxc")
	case "unsuspend":
		return nil
	case "terminate":
		err = p.qemuPoweroff(serviceId, true, vmType == "lxc")
		if err != nil && !strings.Contains(err.Error(), "not running") {
			// ignore error caused by VM not running
			return fmt.Errorf("terminate: force poweroff: %w", err)
		}
		return p.qemuDelete(serviceId, vmType == "lxc")
	case "create":
		return p.createService(serviceId)
	case "boot":
		return p.qemuStart(serviceId, vmType == "lxc")
	}

	return fmt.Errorf("invalid action \"%s\"", action)
}

func (p *PVE) ClientActions(serviceId int32) ([]string, error) {
	s, err := database.Q.FindServiceById(context.Background(), serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: db: %w", err)
	}
	if _, ok := s.Settings["server"]; ok {
		return []string{"poweroff", "reboot", "force_poweroff", "boot"}, nil
	}
	return []string{}, nil

}

func (p *PVE) AdminActions(serviceId int32) ([]string, error) {
	s, err := database.Q.FindServiceById(context.Background(), serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: db: %w", err)
	}
	if _, ok := s.Settings["server"]; ok {
		return []string{"poweroff", "reboot", "terminate", "suspend", "unsuspend", "create", "force_poweroff", "boot"}, nil
	}
	return []string{"create"}, nil
}

func (p *PVE) Route(r chi.Router) error {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello, world")
	})
	r.Get("/novnc", func(w http.ResponseWriter, r *http.Request) {

		jwt := r.URL.Query().Get("jwt")

		// verify jwt

		jwtClaims, err := utils.JWTVerify(jwt)
		if err != nil || jwtClaims["aud"] != "pve_vnc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		r.Header.Set("Content-Type", "text/html")
		err = p.vncPage.Execute(w, map[string]string{
			"JWT":      jwt,
			"Password": jwtClaims["vnc_ticket"].(string),
		})
		if err != nil {
			slog.Error("failed to execute vnc page template", "err", err)
		}

	})
	r.Get("/vncwebsocket", func(w http.ResponseWriter, r *http.Request) {

		// verify jwt

		jwtClaims, err := utils.JWTVerify(r.URL.Query().Get("jwt"))
		if err != nil || jwtClaims["aud"] != "pve_vnc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		vncPort := jwtClaims["vnc_port"].(string)
		vncTicket := jwtClaims["vnc_ticket"].(string)

		serviceId, err := strconv.Atoi(jwtClaims["sub"].(string))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		serviceSettings, serverSettings, err := p.getServiceSettings(int32(serviceId))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			slog.Error("vnc websocket: get service settings", "err", err)
			return
		}

		address := serverSettings["address"]
		port := serverSettings["port"]
		node := serverSettings["node"]
		vmType := serviceSettings["vm_type"]
		vmid := int(10000 + serviceId)
		username := serverSettings["username"]
		password := serverSettings["password"]
		baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

		if vmType == "kvm" {
			vmType = "qemu"
		}

		_, ticket, err := p.pveAuth(baseUrl, username, password)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			slog.Error("pve vnc auth", "err", err, "base", baseUrl, "service id", serviceId)
			return
		}

		// proxy websocket

		pveWebsocketUrl := &url.URL{
			Scheme:   "wss",
			Host:     fmt.Sprintf("%s:%s", address, port),
			Path:     fmt.Sprintf("/api2/json/nodes/%s/%s/%d/vncwebsocket", node, vmType, vmid),
			RawQuery: fmt.Sprintf("port=%s&vncticket=%s", vncPort, url.QueryEscape(vncTicket)),
		}

		wsConn, err := pveWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Debug("websocket upgrade", "err", err)
			return
		}
		defer wsConn.Close()

		pveWsConn, pveWsResp, err := pveWebsocketDialer.Dial(pveWebsocketUrl.String(), http.Header{
			"Cookie": []string{fmt.Sprintf("PVEAuthCookie=%s", ticket)},
		})
		if err != nil {
			if errors.Is(err, websocket.ErrBadHandshake) {
				body, _ := io.ReadAll(pveWsResp.Body)
				slog.Error("pve websocket dial bad handshake", "status", pveWsResp.Status, "body", string(body), "service id", serviceId)
			}
			slog.Error("pve websocket dial", "err", err, "service id", serviceId)
			return
		}

		defer pveWsConn.Close()

		var wg sync.WaitGroup
		wg.Add(2)

		wsCopy := func(dst *websocket.Conn, src *websocket.Conn) {
			defer wg.Done()
			defer src.Close()
			defer dst.Close()
			for {
				src.SetReadDeadline(time.Now().Add(time.Minute))
				mt, message, err := src.ReadMessage()
				if err != nil {
					slog.Debug("websocket read", "err", err, "src", dst.RemoteAddr().String())
					break
				}
				dst.SetWriteDeadline(time.Now().Add(time.Minute))
				err = dst.WriteMessage(mt, message)
				if err != nil {
					slog.Debug("websocket write", "err", err, "src", dst.RemoteAddr().String())
					break
				}
			}
		}

		go wsCopy(pveWsConn, wsConn)
		go wsCopy(wsConn, pveWsConn)

		wg.Wait()

	})
	return nil
}

func (p *PVE) getQemuVmInfo(serviceId int32) (*pveVmInfo, error) {
	serviceSettings, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	vmType := serviceSettings["vm_type"]
	if vmType == "kvm" {
		vmType = "qemu"
	}

	vmInfo := pveVmInfo{}

	address := serverSettings["address"]
	port := serverSettings["port"]
	username := serverSettings["username"]
	password := serverSettings["password"]
	node := serverSettings["node"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	_, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	vmid := int(10000 + serviceId)

	respStatus := pveResp[struct {
		Status  string `json:"status"`
		MaxDisk int    `json:"maxdisk"`
		MaxMem  int    `json:"maxmem"`
		Name    string `json:"Name"`
	}]{}
	err = p.apiGet(fmt.Sprintf("%s/nodes/%s/%s/%d/status/current", baseUrl, node, vmType, vmid), &respStatus, ticket)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	vmInfo.Name = respStatus.Data.Name
	vmInfo.Status = respStatus.Data.Status
	vmInfo.MaxDisk = respStatus.Data.MaxDisk / 1000 / 1000 / 1000
	vmInfo.MaxMemory = respStatus.Data.MaxMem / 1024 / 1024

	respConfig := pveResp[struct {
		Cores     int    `json:"cores"`
		IPConfig0 string `json:"ipconfig0"`
		CiUser    string `json:"ciuser"`
		Net0      string `json:"net0"`
	}]{}
	err = p.apiGet(fmt.Sprintf("%s/nodes/%s/%s/%d/config", baseUrl, node, vmType, vmid), &respConfig, ticket)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	if vmType == "lxc" {
		// lxc network config

		matches := regexp.MustCompile(`gw=(\d+\.\d+\.\d+\.\d+)`).FindStringSubmatch(respConfig.Data.Net0)
		if matches != nil {
			vmInfo.IPv4Gateway = matches[1]
		}
		matches = regexp.MustCompile(`ip=(\d+\.\d+\.\d+\.\d+/\d+)`).FindStringSubmatch(respConfig.Data.Net0)
		if matches != nil {
			vmInfo.IPv4 = matches[1]
		}
	} else {

		// kvm network config

		for _, s := range strings.Split(respConfig.Data.IPConfig0, ",") {
			if strings.HasPrefix(s, "gw=") {
				vmInfo.IPv4Gateway = strings.TrimPrefix(s, "gw=")
			}
			if strings.HasPrefix(s, "ip=") {
				vmInfo.IPv4 = strings.TrimPrefix(s, "ip=")
			}
		}
	}

	vmInfo.Cores = respConfig.Data.Cores

	if vmType == "lxc" {
		vmInfo.Username = "root"
	} else {
		vmInfo.Username = respConfig.Data.CiUser
	}
	vmInfo.Password = serviceSettings["vm_password"]

	return &vmInfo, nil
}

func (p *PVE) ClientPage(w http.ResponseWriter, r *http.Request, serviceId int32) error {
	return p.AdminPage(w, r, serviceId)
}

func (p *PVE) AdminPage(w http.ResponseWriter, r *http.Request, serviceId int32) error {

	serviceSettings, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		if errors.Is(err, errNoServerAssigned) {
			io.WriteString(w, "<span style=\"font-family: sans-serif\">This service is not created</span>")
			return nil
		}
		w.WriteHeader(http.StatusInternalServerError)
		return err
	}

	vmType := serviceSettings["vm_type"]
	if vmType == "kvm" {
		vmType = "qemu"
	}

	// find the list of available operating systems

	templateListKey := "lxc_template_list"
	templateKey := "lxc_template"
	if vmType == "qemu" {
		templateListKey = "kvm_template_list"
		templateKey = "kvm_template_vmid"
	}

	templateListString, ok := serviceSettings[templateListKey]
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return fmt.Errorf("%s is not set in service settings", templateListKey)
	}

	if r.Method == "POST" {
		type actionForm struct {
			Action string `json:"action"`
			OS     string `json:"os"`
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return nil
		}

		var form actionForm
		err := json.NewDecoder(r.Body).Decode(&form)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil
		}

		switch form.Action {
		case "reinstall":

			// reinstall

			selectedOsValue := ""

			// validate that the user submitted os in the the list

			for line := range strings.SplitSeq(templateListString, "\n") {
				// format: "Display name|value(vmid or lxc template)"
				// lxc example: "Ubuntu 22.04 LTS|local:vztmpl/ubuntu-22.04-standard_22.04-1_amd64.tar.gz"
				// kvm example: "Ubuntu 22.04 LTS|100"
				parts := strings.SplitN(line, "|", 2)
				if len(parts) != 2 {
					continue
				}
				value := strings.TrimSpace(parts[1])
				if form.OS == value {
					selectedOsValue = value
					break
				}
			}

			if selectedOsValue == "" {
				io.WriteString(w, "{\"error\": \"The selected OS is unavailable\"}")
				return nil
			}

			// update service settings

			serviceSettings[templateKey] = selectedOsValue
			err = database.Q.UpdateServiceSettings(r.Context(), database.UpdateServiceSettingsParams{
				ID:       serviceId,
				Settings: serviceSettings,
			})
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return err
			}

			slog.Info("reinstall request lxc", "service id", serviceId, "os", selectedOsValue)

			// schedule reinstall action

			err := DoActionAsync(r.Context(), "PVE", serviceId, "reinstall", "")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return err
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "{\"ok\": true}")
			return nil

		case "vnc":

			slog.Info("vnc request", "service id", serviceId)

			// get pve websocket url

			slog.Info("vnc websocket", "service id", serviceId)

			address := serverSettings["address"]
			port := serverSettings["port"]
			username := serverSettings["username"]
			password := serverSettings["password"]
			node := serverSettings["node"]
			baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

			csrf, ticket, err := p.pveAuth(baseUrl, username, password)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				slog.Error("pve vnc auth", "err", err, "base", baseUrl, "service id", serviceId)
				return nil
			}

			vmid := int(10000 + serviceId)

			resp := pveResp[struct {
				Port   string `json:"port"`
				Ticket string `json:"ticket"`
			}]{}
			err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/%s/%d/vncproxy", baseUrl, node, vmType, vmid), url.Values{"websocket": []string{"1"}}, &resp, csrf, ticket)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				slog.Error("pve vnc proxy", "err", err, "base", baseUrl, "service id", serviceId)
				return nil
			}

			claims := jwt.MapClaims{
				"aud":        "pve_vnc",
				"sub":        strconv.Itoa(int(serviceId)),
				"vnc_port":   resp.Data.Port,
				"vnc_ticket": resp.Data.Ticket,
			}

			jwtToken := utils.JWTSign(claims, time.Minute)

			respJson := map[string]any{
				"jwt": jwtToken,
				"ok":  true,
			}
			respBytes, _ := json.Marshal(respJson)

			w.Header().Set("Content-Type", "application/json")
			w.Write(respBytes)
			return nil

		default:

			w.WriteHeader(http.StatusBadRequest)
			return nil

		}
	}

	operatingSystems := make([][]string, 0)
	for line := range strings.SplitSeq(templateListString, "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		displayName := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		operatingSystems = append(operatingSystems, []string{displayName, value})
	}

	info, err := p.getQemuVmInfo(serviceId)
	if err != nil {
		if errors.Is(err, errNoServerAssigned) {
			io.WriteString(w, "<span style=\"font-family: sans-serif\">This service is not created</span>")
			return nil
		}
		io.WriteString(w, "<span style=\"font-family: sans-serif\">Something went wrong. Please check server log.</span>")
		return err
	}

	info.OS = operatingSystems

	err = p.infoPage.Execute(w, info)
	if err != nil {
		return err
	}

	return nil
}

func (p *PVE) Init() error {
	p.httpClient = http.Client{
		Timeout: time.Second * 10,
		Transport: &http.Transport{
			TLSHandshakeTimeout: time.Second * 5,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	p.infoPage = template.Must(template.New("pve_info").Parse(pveInfoHtml))
	p.vncPage = template.Must(template.New("pve_vnc").Parse(pveVncHtml))
	return nil
}

func (p *PVE) ProductSettings(inputs map[string]string) ([]ProductSetting, error) {
	s := []ProductSetting{
		{Name: "servers", DisplayName: "Servers", Type: "servers"},
		{Name: "disk", DisplayName: "Disk (GB)", Type: "string", Regex: "^\\d+$"},
		{Name: "memory", DisplayName: "Memory (MB)", Type: "string", Regex: "^\\d+$"},
		{Name: "cpu", DisplayName: "CPU Cores", Type: "string", Regex: "^\\d+$"},
		{Name: "vm_password", DisplayName: "VM Password (Can be overwritten by options)", Type: "string", Regex: "^.+$"},
		{Name: "vm_type", DisplayName: "VM Type", Type: "select", Values: []string{"kvm", "lxc"}},
	}

	vmType, _ := inputs["vm_type"]
	switch vmType {
	case "kvm":
		s = append(s, ProductSetting{Name: "kvm_template_vmid", DisplayName: "KVM Template VMID", Type: "string", Regex: "^\\d+$"})
		s = append(s, ProductSetting{Name: "kvm_template_list", Description: "List of KVM template VM that the user can choose to reinstall from. One per line, in the form of [display name]|[template vm id]. New VMs are created by cloning the template VM selected by the client.", Placeholder: "Debian 13|100\nDebian 12|101\nAlmaLinux 10|102...", DisplayName: "List of KVM templates", Type: "text", Regex: "."})

	case "lxc":
		s = append(s, ProductSetting{Name: "lxc_template", Placeholder: "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst", DisplayName: "LXC Template", Type: "string", Regex: "^.+$"})
		s = append(s, ProductSetting{Name: "lxc_template_list", Description: "List of LXC templates that the user can choose to reinstlal from. One per line, in the form of [display name]|[template location]", Placeholder: "Debian 13|local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst\nDebian 12|local:vztmpl/debian-12-standard_12.12-1_amd64.tar.zst\nAlmaLinux 10|local:vztmpl/almalinux-10-default_20250930_amd64.tar.xz\n...", DisplayName: "List of LXC templates", Type: "text", Regex: "."})
	}

	return s, nil
}

func (p *PVE) ServerSettings() []ServerSettings {
	return []ServerSettings{
		{Name: "address", DisplayName: "Address", Type: "string", Placeholder: "8.8.8.8", Regex: "^.+$"},
		{Name: "port", DisplayName: "Port", Type: "string", Placeholder: "8006", Regex: "^\\d+$"},
		{Name: "username", DisplayName: "Username", Type: "string", Placeholder: "root@pam", Regex: "^.+$"},
		{Name: "password", DisplayName: "Password", Type: "string", Regex: "^.+$"},
		{Name: "node", DisplayName: "Node", Type: "string", Regex: "^.+$"},
		{Name: "bridge", DisplayName: "Network device bridge", Type: "string", Regex: "^.+$", Placeholder: "vmbr0"},
		{Name: "gateway", DisplayName: "IPv4 Gateway", Type: "string", Regex: "^.+$"},
		{Name: "ips", DisplayName: "IPv4 Addresses (one per line)", Type: "text", Regex: "^(.|\\n)+$", Placeholder: "10.2.3.100/24\n10.2.3.101/24\n10.2.3.102/24\n10.2.3.103/24"},
	}
}

func init() {
	registerExtension("PVE", &PVE{})
}
