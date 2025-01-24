package controller

import (
	"billing3/controller/middlewares"
	"billing3/service/extension"
	"billing3/service/gateways"
	"github.com/go-chi/chi/v5"
	"log/slog"
	"net/http"
	"strings"
)

func Route(r *chi.Mux) {
	r.Use(middlewares.Auth)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello, world"))
	})

	// auth
	r.Group(func(r chi.Router) {
		r.Post("/auth/login", login)
		r.Post("/auth/register", register)
		r.Post("/auth/register2", registerStep2)
		r.With(middlewares.MustAuth).Get("/auth/me", me)
	})

	// admin
	r.Group(func(r chi.Router) {
		r.Use(middlewares.MustAuth)
		r.Use(middlewares.RequireRole("admin"))

		r.Get("/admin/user", adminUserList)
		r.Post("/admin/user", adminUserCreate)
		r.Put("/admin/user/{id}", adminUserEdit)
		r.Get("/admin/user/{id}", adminUserGet)

		r.Get("/admin/category", adminCategoryList)
		r.Post("/admin/category", adminCategoryCreate)
		r.Put("/admin/category/{id}", adminCategoryUpdate)
		r.Get("/admin/category/{id}", adminCategoryGet)
		r.Delete("/admin/category/{id}", adminCategoryDelete)

		r.Get("/admin/product", adminProductList)
		r.Get("/admin/product/extension-list", adminProductExtensionList)
		r.Post("/admin/product/extension-settings", adminProductExtensionSettings)
		r.Post("/admin/product", adminProductCreate)
		r.Put("/admin/product/{id}", adminProductUpdate)
		r.Get("/admin/product/{id}", adminProductGet)
		r.Delete("/admin/product/{id}", adminProductDelete)

		r.Get("/admin/invoice", adminInvoiceList)
		r.Get("/admin/invoice/{id}", adminInvoiceGet)
		r.Put("/admin/invoice/{id}", adminInvoiceEdit)
		r.Post("/admin/invoice/{id}/item", adminInvoiceAddItem)
		r.Delete("/admin/invoice/{id}/item/{item_id}", adminInvoiceRemoveItem)
		r.Put("/admin/invoice/{id}/item/{item_id}", adminInvoiceUpdateItem)
		r.Get("/admin/invoice/{id}/payment", adminListInvoicePayment)
		r.Post("/admin/invoice/{id}/payment", adminAddInvoicePayment)

		r.Get("/admin/gateway", adminListGateways)
		r.Get("/admin/gateway/{id}", adminGatewayGet)
		r.Put("/admin/gateway/{id}", adminGatewayUpdate)
		r.Get("/admin/gateway/settings", adminGatewaySettings)

		r.Get("/admin/service", adminServiceList)
		r.Get("/admin/service/{id}", adminServiceGet)
		r.Put("/admin/service/{id}", adminServiceUpdate)
		r.Get("/admin/service/{id}/invoice", adminInvoiceListByService)
		r.Post("/admin/service/{id}/invoice", adminServiceGenerateInvoice)
		r.Get("/admin/service/{id}/action", adminServiceActions)
		r.Post("/admin/service/{id}/action", adminServicePerformAction)
		r.Get("/admin/service/{id}/info", adminServiceInfoPage)
		r.Put("/admin/service/{id}/status", adminServiceUpdateStatus)
		r.Get("/admin/service/{id}/action", adminServiceAction)

		r.Get("/admin/server", adminServerList)
		r.Get("/admin/server/{id}", adminServerGet)
		r.Put("/admin/server/{id}", adminServerEdit)
		r.Post("/admin/server", adminServerAdd)
		r.Delete("/admin/server/{id}", adminServerDelete)
		r.Get("/admin/server/extension-settings", adminExtensionServerSettings)
	})

	// store
	r.Group(func(r chi.Router) {
		r.Get("/store/category", listCategories)
		r.Get("/store/category/{id}", getCategory)
		r.Get("/store/category/{id}/product", listProductByCategory)
		r.Get("/store/product/{id}", getProduct)
		r.Get("/store/product/{id}/options", getProductOptions)
		r.Post("/store/calculate-price", calculatePrice)
		r.With(middlewares.MustAuth).Post("/store/order", order)
	})

	// user
	r.Group(func(r chi.Router) {
		r.Use(middlewares.MustAuth)

		r.Get("/invoice", listInvoices)
		r.Get("/invoice/{id}", getInvoice)
		r.Get("/invoice/gateways", getAvailablePaymentGateways)
		r.Post("/invoice/{id}/pay", makePayment)
		r.Get("/invoice/{id}/payments", getInvoicePayments)
	})

	for name, gateway := range gateways.Gateways {
		router := chi.NewRouter()

		err := gateway.Route(router)
		if err != nil {
			slog.Error("register gateway routes", "gateway", name, "err", err)
			panic(err)
		}

		prefix := "/gateway/" + strings.ToLower(name)
		r.Mount(prefix, router)

		slog.Info("register gateway routes", "pattern", prefix)

		_ = chi.Walk(router, func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
			slog.Info("gateway route", "method", method, "route", prefix+route)
			return nil
		})
	}

	for name, ext := range extension.Extensions {
		router := chi.NewRouter()

		err := ext.Route(router)
		if err != nil {
			slog.Error("register extension routes", "extension", name, "err", err)
			panic(err)
		}

		prefix := "/extension/" + strings.ToLower(name)
		r.Mount(prefix, router)

		slog.Info("register extension routes", "pattern", prefix)

		_ = chi.Walk(router, func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
			slog.Info("extension route", "method", method, "route", prefix+route)
			return nil
		})
	}
}
