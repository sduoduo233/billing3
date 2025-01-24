package extension

import (
	"billing3/database"
	"billing3/utils"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type PVE struct {
	httpClient http.Client
}

type pveResp[T any] struct {
	Data T `json:"data"`
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

	resp := pveResp[struct {
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Ticket              string `json:"ticket"`
		Username            string `json:"username"`
	}]{}
	err = json.NewDecoder(httpResp.Body).Decode(&resp)
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

	err = json.NewDecoder(httpResp.Body).Decode(resp)
	if err != nil {
		return fmt.Errorf("api get: %w", err)
	}
	return nil
}

func (p *PVE) apiPost(api string, body url.Values, resp any, csrf, ticket string) error {
	req, err := http.NewRequest("POST", api, strings.NewReader(body.Encode()))
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

	err = json.NewDecoder(httpResp.Body).Decode(resp)
	if err != nil {
		return fmt.Errorf("api post: %w", err)
	}

	return nil
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
		s, err := database.Q.FindServiceById(context.Background(), serviceId)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		cpu := s.Settings["cpu"]
		disk := s.Settings["disk"]
		memory := s.Settings["memory"]
		servers := s.Settings["servers"]
		vmType := s.Settings["vm_type"]
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
		serverId := serverIds[utils.Randint(0, len(serverIds)-1)]

		// pve server settings
		server, err := database.Q.FindServerById(context.Background(), int32(serverId))
		if err != nil {
			return fmt.Errorf("pve: invalid servers: %d %w", serverId, err)
		}

		address := server.Settings["address"]
		port := server.Settings["port"]
		username := server.Settings["username"]
		password := server.Settings["password"]
		node := server.Settings["node"]
		_ = server.Settings["ips"]
		baseUrl := fmt.Sprintf("https://%s:%s/api2/json", address, port)

		slog.Info("pve create", "server id", serverId, "servers", servers, "cpu", cpu, "disk", disk, "memory", memory, "pve base", baseUrl, "node", node, "vm type", vmType, "kvm template vmid", kvmTemplateVmid)

		// pve auth
		_, _, err = p.pveAuth(baseUrl, username, password)
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		// calculate new vmid
		_ = 200000 + serviceId

		// TODO:

		// save server id
		s.Settings["server"] = strconv.Itoa(serverId)

		err = database.Q.UpdateServiceSettings(context.Background(), database.UpdateServiceSettingsParams{
			ID:       int32(serviceId),
			Settings: s.Settings,
		})
		if err != nil {
			return fmt.Errorf("pve: %w", err)
		}

		return nil
	}

	return fmt.Errorf("invalid action \"%s\"", action)
}

func (p *PVE) ClientActions(serviceId int32) ([]string, error) {
	//TODO implement me
	panic("implement me")
}

func (p *PVE) AdminActions(serviceId int32) ([]string, error) {
	return []string{"poweroff", "reboot", "terminate", "suspend", "unsuspend", "create"}, nil
}

func (p *PVE) Route(r chi.Router) error {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello, world")
	})
	return nil
}

func (p *PVE) ClientPage(w http.ResponseWriter, serviceId int32) error {
	//TODO implement me
	panic("implement me")
}

func (p *PVE) AdminPage(w http.ResponseWriter, serviceId int32) error {
	io.WriteString(w, "<p>Memory: 123MB / 1024MB</p><p>Bandwidth: 1G / 1024G</p><p>Disk: 5G / 20G</p><p>CPU: 30%</p>")
	return nil
}

func (p *PVE) Init() error {
	p.httpClient = http.Client{
		Timeout: time.Second * 10,
	}
	return nil
}

func (p *PVE) ProductSettings(inputs map[string]string) ([]ProductSetting, error) {
	s := []ProductSetting{
		{Name: "servers", DisplayName: "Servers", Type: "servers"},
		{Name: "disk", DisplayName: "Disk (GB)", Type: "string", Regex: "^\\d+"},
		{Name: "memory", DisplayName: "Memory (MB)", Type: "string", Regex: "^\\d+"},
		{Name: "cpu", DisplayName: "CPU Cores", Type: "string", Regex: "^\\d+"},
		{Name: "vm_type", DisplayName: "VM Type", Type: "select", Values: []string{"kvm", "lxc"}},
	}

	vmType, _ := inputs["vm_type"]
	if vmType == "kvm" {
		s = append(s, ProductSetting{Name: "kvm_template_vmid", DisplayName: "KVM Template VMID", Type: "string", Regex: "^\\d+"})
	} else if vmType == "lxc" {
		s = append(s, ProductSetting{Name: "lxc_template", DisplayName: "LXC Template", Type: "string", Regex: "^.+"})
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
		{Name: "ips", DisplayName: "IP Addresses (one per line)", Type: "text", Placeholder: "10.2.3.100/24\n10.2.3.101/24\n10.2.3.102/24\n10.2.3.103/24"},
	}
}

func init() {
	registerExtension("PVE", &PVE{})
}
