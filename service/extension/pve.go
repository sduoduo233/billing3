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
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	_ "embed"
)

//go:embed pveinfo.html
var pveInfoHtml string

var errNoServerAssigned = errors.New("no server assigned")

type PVE struct {
	httpClient http.Client
	infoPage   *template.Template
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
	kvmTemplateVmid := s.Settings["kvm_template_vmid"]

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

	// lock the server
	err = utils.Lock(fmt.Sprintf("server_%d", serviceId))
	if err != nil {
		return fmt.Errorf("pve: server lock: %w", err)
	}
	defer func() {
		// unlock
		err := utils.Unlock(fmt.Sprintf("server_%d", serviceId))
		if err != nil {
			slog.Error("pve: server unlock", "err", err)
		}
	}()

	// pve server settings
	server, err := database.Q.FindServerById(ctx, int32(serverId))
	if err != nil {
		return fmt.Errorf("pve: invalid servers: %d %w", serverId, err)
	}

	address := server.Settings["address"]
	port := server.Settings["port"]
	username := server.Settings["Username"]
	password := server.Settings["password"]
	node := server.Settings["node"]
	ips := server.Settings["ips"]
	gateway := server.Settings["gateway"]
	baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

	// choose an IP address
	unusedIps := utils.Filter(strings.Split(ips, "\n"), func(ip string) bool {
		// ip with # prefix are currently in use
		return len(ip) != 0 && ip[0] != '#'
	})
	if len(unusedIps) == 0 {
		return fmt.Errorf("pve: server %d: no unused ips available", serverId)
	}
	ip, _ := utils.RandomChoose(unusedIps)

	slog.Info("pve create", "server id", serverId, "servers", servers, "cpu", cpu, "disk", disk, "memory", memory, "pve base", baseUrl, "node", node, "vm type", vmType, "kvm template vmid", kvmTemplateVmid, "ip", ip)

	// pve auth
	csrf, ticket, err := p.pveAuth(baseUrl, username, password)
	if err != nil {
		return fmt.Errorf("pve: %w", err)
	}

	// vmid
	vmid := int(10000 + serviceId)

	// clone vm
	resp := pveResp[string]{}
	form := url.Values{}
	form.Set("newid", strconv.Itoa(vmid))
	form.Set("full", "1")
	form.Set("name", fmt.Sprintf("service%d", serviceId))
	err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/qemu/%s/clone", baseUrl, node, kvmTemplateVmid), form, &resp, csrf, ticket)
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
	err = p.apiAction("POST", fmt.Sprintf("%s/nodes/%s/qemu/%d/config", baseUrl, node, vmid), form, &resp, csrf, ticket)
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

	// save server id
	s.Settings["server"] = strconv.Itoa(serverId)
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
			newIps += ip + "\n"
		}
	}
	server.Settings["ips"] = newIps
	err = database.Q.UpdateServerSettings(ctx, database.UpdateServerSettingsParams{
		ID:       int32(vmid),
		Settings: server.Settings,
	})
	if err != nil {
		return fmt.Errorf("pve: db: %w", err)
	}

	return nil
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

func (p *PVE) Action(serviceId int32, action string) error {
	slog.Info("pve action", "service id", serviceId, "action", action)

	switch action {
	case "poweroff":
		return nil
	case "reboot":
		return nil
	case "suspend":
		return nil
	case "unsuspend":
		return nil
	case "terminate":
		return nil
	case "create":
		return p.createService(serviceId)
	}

	return fmt.Errorf("invalid action \"%s\"", action)
}

func (p *PVE) ClientActions(serviceId int32) ([]string, error) {
	s, err := database.Q.FindServiceById(context.Background(), serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: db: %w", err)
	}
	if _, ok := s.Settings["server"]; ok {
		return []string{"poweroff", "reboot"}, nil
	}
	return []string{}, nil

}

func (p *PVE) AdminActions(serviceId int32) ([]string, error) {
	s, err := database.Q.FindServiceById(context.Background(), serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: db: %w", err)
	}
	if _, ok := s.Settings["server"]; ok {
		return []string{"poweroff", "reboot", "terminate", "suspend", "unsuspend", "create"}, nil
	}
	return []string{"create"}, nil
}

func (p *PVE) Route(r chi.Router) error {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello, world")
	})
	return nil
}

func (p *PVE) getQemuVmInfo(serviceId int32) (*pveVmInfo, error) {
	_, serverSettings, err := p.getServiceSettings(serviceId)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
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
	err = p.apiGet(fmt.Sprintf("%s/nodes/%s/qemu/%d/status/current", baseUrl, node, vmid), &respStatus, ticket)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	vmInfo.Name = respStatus.Data.Name
	vmInfo.Status = respStatus.Data.Status
	vmInfo.MaxDisk = respStatus.Data.MaxDisk / 1024 / 1024 / 1024
	vmInfo.MaxMemory = respStatus.Data.MaxMem / 1024 / 1024

	respConfig := pveResp[struct {
		Cores     int    `json:"cores"`
		IPConfig0 string `json:"ipconfig0"`
		CiUser    string `json:"ciuser"`
	}]{}
	err = p.apiGet(fmt.Sprintf("%s/nodes/%s/qemu/%d/config", baseUrl, node, vmid), &respConfig, ticket)
	if err != nil {
		return nil, fmt.Errorf("pve: %w", err)
	}

	vmInfo.Cores = respConfig.Data.Cores
	vmInfo.Username = respConfig.Data.CiUser
	for _, s := range strings.Split(respConfig.Data.IPConfig0, ",") {
		if strings.HasPrefix(s, "gw=") {
			vmInfo.IPv4Gateway = strings.TrimPrefix(s, "gw=")
		}
		if strings.HasPrefix(s, "ip=") {
			vmInfo.IPv4 = strings.TrimPrefix(s, "ip=")
		}
	}

	return &vmInfo, nil
}

func (p *PVE) ClientPage(w http.ResponseWriter, serviceId int32) error {
	return p.AdminPage(w, serviceId)
}

func (p *PVE) AdminPage(w http.ResponseWriter, serviceId int32) error {
	info, err := p.getQemuVmInfo(serviceId)
	if err != nil {
		if errors.Is(err, errNoServerAssigned) {
			io.WriteString(w, "<span style=\"font-family: sans-serif\">This service is not created</span>")
			return nil
		}
		io.WriteString(w, "<span style=\"font-family: sans-serif\">Something went wrong</span>")
		return err
	}

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
	if vmType == "kvm" {
		s = append(s, ProductSetting{Name: "kvm_template_vmid", DisplayName: "KVM Template VMID", Type: "string", Regex: "^\\d+$"})
	} else if vmType == "lxc" {
		s = append(s, ProductSetting{Name: "lxc_template", DisplayName: "LXC Template", Type: "string", Regex: "^.+$"})
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
