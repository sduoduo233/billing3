# billing3 Billing Logic Documentation

## Configurable options

![](/docs/product_options.png)

- A product can be associated with an **extension**.
- The extension defines a list of **options** (settings) required for configuration (e.g., server location, OS template).
- When an admin creates a product, they must fill in these options as defined by the extension. These become the default settings for the product.

![](/docs/product_co.png)

- A product can also have **Configurable Options (COs)**. These are options that the user can select during the ordering process (e.g., extra RAM, specific OS).

![](/docs/service_settings.png)

- When a user orders a product, a **service** is created.
- The options defined in the product settings are copied to the service settings.
- If the user selected any Configurable Options (COs), these values are also copied to the service settings.
- **Important:** Configurable Options (COs) **override** the default options from the product settings. If a CO has the same name as a product setting, the CO value takes precedence.

## Order lifecycle

### 1. Order Placement
When a client places an order:
1.  **Validation**: The system checks if the product is enabled and in stock.
2.  **Pricing**: The recurring fee and setup fee are calculated based on the selected billing cycle and configurable options.
3.  **Service Creation**: A new service is created in the database with:
    -   **Status**: `UNPAID`
    -   **ExpiresAt**: Set to the current time (effectively expired immediately until paid).
    -   **Settings**: A merge of the product's default settings and the user's selected options (options override defaults).
4.  **Invoice Creation**: An initial invoice is created for the service.
    -   **Status**: `UNPAID`
    -   **Due Date**: 7 days from creation.
    -   **Items**: Includes the service's recurring fee for the first billing cycle and any one-time setup fee.

### 2. Payment and Activation
When the client pays the invoice (e.g., via a payment gateway):
1.  **Invoice Status**: The invoice status changes to `PAID`.
2.  **Expiry Extension**: The service's `ExpiresAt` date is extended by the duration of the billing cycle (e.g., +1 month).
3.  **Status Transition**:
    -   If the service status was `UNPAID`, it changes to `PENDING`.
4.  **Provisioning**: The system asynchronously triggers the `create` action for the associated extension (e.g., PVE).
    -   Upon successful provisioning, the service status changes to `ACTIVE`.

### 3. Provisioning (PVE Example)
When the `create` action is triggered for a PVE (Proxmox VE) service:
1.  **Server Selection**: The system looks at the `servers` setting (a list of server IDs) associated with the product. It randomly selects one server from this list.
2.  **IP Allocation**:
    -   If an IP is not already assigned, the system randomly selects an unused IP from the selected server's `ips` pool.
    -   The selected IP is marked as used.
3.  **VM Creation**:
    -   The system connects to the selected Proxmox node.
    -   It creates a new VM (KVM clone or LXC container) based on the template defined in the product settings.
    -   Resources (CPU, RAM, Disk) and network settings (IP, Gateway) are configured.
4.  **Completion**: The service settings are updated with the assigned `server` ID and `ip` address.

### 4. Expiration and Overdue Handling
The system runs hourly cron jobs to handle overdue items:

-   **Overdue Invoices**:
    -   If an invoice is past its `DueAt` date (7 days), it is marked as `CANCELLED` (Reason: "overdue").
    -   If the invoice was for an `UNPAID` service (e.g., the initial order), the service is also marked as `CANCELLED` (Reason: "invoice overdue").

-   **Overdue Services**:
    -   If a service is past its `ExpiresAt` date, it is considered overdue.
    -   The system triggers the `terminate` action for the extension to destroy the resources (e.g., delete the VM).
    -   The service status is changed to `CANCELLED`.

### 5. Renewal
-   **Invoice Generation**: A daily cron job checks for services expiring within the next 5 days.
    -   If a service is `ACTIVE` or `SUSPENDED` and does not already have an unpaid renewal invoice, a new invoice is generated.
    -   The invoice includes the recurring fee for the next billing cycle.
    -   **Due Date**: 7 days from creation.
-   **Payment**: When the renewal invoice is paid, the service's `ExpiresAt` date is extended by the billing cycle duration.

