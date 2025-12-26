package gateways

import (
	"billing3/database"
	"context"
	"log/slog"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

var Gateways = make(map[string]Gateway)
var PUBLIC_DOMAIN string

type GatewaySetting struct {
	DisplayName string   `json:"display_name"` // the name that will be displayed on frontend
	Name        string   `json:"name"`         // the name that will be used in database
	Placeholder string   `json:"placeholder"`  // placeholder text
	Type        string   `json:"type"`         // string (single line) / text (multiple lines) / select
	Values      []string `json:"values"`       // values for select (ignored if Type is not select)
	Description string   `json:"description"`  // helper text
	Regex       string   `json:"regex"`        // regex for validating input
}

type Gateway interface {
	// Settings returns a list of configurable settings
	Settings() []GatewaySetting

	// Pay initialize a one time payment, and returns a URL that the user will be redirected to.
	// total is the total amount, including the payment gateway fee.
	Pay(invoice *database.Invoice, user *database.User, total decimal.Decimal) (string, error)

	// Route is called once when the application starts.
	// The payment gateway may register custom routes to r.
	Route(r chi.Router) error
}

func registerGateway(name string, extension Gateway) {
	slog.Info("gateway registered", "name", name)
	Gateways[name] = extension
}

func InitGateways() error {
	ctx := context.Background()

	PUBLIC_DOMAIN = os.Getenv("PUBLIC_DOMAIN")
	if PUBLIC_DOMAIN == "" {
		panic("PUBLIC_DOMAIN environment variable is not set")
	}

	// initialize gateways settings
	for name := range Gateways {
		err := database.Q.CreateGatewayOrIgnore(ctx, name)
		if err != nil {
			return err
		}
	}

	// delete invalid gateways
	names, err := database.Q.ListGatewayNames(ctx)
	if err != nil {
		return err
	}

	for _, name := range names {
		_, ok := Gateways[name]
		if !ok {
			err := database.Q.DeleteGatewayByName(ctx, name)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
