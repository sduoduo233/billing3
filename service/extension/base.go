package extension

import (
	"billing3/utils"
	"fmt"
	"github.com/go-chi/chi/v5"
	"log/slog"
	"net/http"
)

var Extensions = make(map[string]Extension)
var Queues = make(map[string]*utils.Queue)

type ProductSetting struct {
	DisplayName string   `json:"display_name"` // the name that will be displayed on frontend
	Name        string   `json:"name"`         // the name that will be used in database
	Placeholder string   `json:"placeholder"`  // placeholder text
	Type        string   `json:"type"`         // string (single line) / text (multiple lines) / select / servers
	Values      []string `json:"values"`       // values for select (ignored if Type is not select)
	Description string   `json:"description"`  // helper text
	Regex       string   `json:"regex"`        // regex for validating input
}
type ServerSettings struct {
	DisplayName string   `json:"display_name"` // the name that will be displayed on frontend
	Name        string   `json:"name"`         // the name that will be used in database
	Placeholder string   `json:"placeholder"`  // placeholder text
	Type        string   `json:"type"`         // string (single line) / text (multiple lines) / select
	Values      []string `json:"values"`       // values for select (ignored if Type is not select)
	Description string   `json:"description"`  // helper text
	Regex       string   `json:"regex"`        // regex for validating input
}

type Extension interface {
	// Init initializes the extension.
	// Called when the application starts.
	Init() error

	// ProductSettings returns a list of settings required for
	// configuring a product.
	//
	// Inputs are user inputs so far. ProductSettings may return
	// different settings based on user inputs.
	ProductSettings(inputs map[string]string) ([]ProductSetting, error)

	// ServerSettings returns a list of settings that are necessary to
	// connect to a server (e.g. ip, port, username, password for a
	// ProxmoxVE server)
	ServerSettings() []ServerSettings

	// Action performs an action on the service.
	// Action must support the following actions:
	// suspend, create, terminate, unsuspend
	Action(serviceId int32, action string) error

	// ClientActions returns a list of actions that can be performed
	// by clients, considering the current status of the service.
	// (e.g. poweroff, reboot)
	ClientActions(serviceId int32) ([]string, error)

	// AdminActions returns a list of actions that can be performed
	// by admins, considering the current status of the service.
	// (e.g. suspend, unsuspend, terminate, create, poweroff, reboot)
	AdminActions(serviceId int32) ([]string, error)

	// Route is called once when the application starts.
	// The extension may register custom routes to r.
	Route(r chi.Router) error

	// ClientPage renders a complete html page that contains information
	// about the service, shown to the client.
	ClientPage(w http.ResponseWriter, serviceId int32) error

	// AdminPage renders a complete html page that contains information
	// about the service, shown to admins.
	AdminPage(w http.ResponseWriter, serviceId int32) error
}

func registerExtension(name string, extension Extension) {
	slog.Info("extension registered", "name", name)
	Extensions[name] = extension
}

func Init() error {
	for name, extension := range Extensions {
		err := extension.Init()
		if err != nil {
			return fmt.Errorf("init %s: %w", name, err)
		}

		Queues[name] = initQueue(name, extension)
	}
	return nil
}
