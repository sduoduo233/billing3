-- USERS --

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: FindUserById :one
SELECT * FROM users WHERE id = $1;

-- name: FindUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: SearchUsersPaged :many
SELECT * FROM users WHERE position(@search::text in email)>0 OR position(@search::text in name)>0 ORDER BY id LIMIT $1 OFFSET $2;

-- name: SearchUsersCount :one
SELECT count(*) FROM users WHERE position(@search::text in email)>0 OR position(@search::text in name)>0;

-- name: CreateUser :one
INSERT INTO users (email, name, role, password, address, city, state, country, zip_code) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: UpdateUser :exec
UPDATE users SET email = $2, name = $3, role = $4, address = $5, city = $6, state = $7, country = $8, zip_code = $9 WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password = $2 WHERE id = $1;

-- SESSIONS --

-- name: FindSessionByToken :one
SELECT * FROM sessions WHERE token = $1 AND expires_at > CURRENT_TIMESTAMP;

-- name: CreateSession :exec
INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token = $1;

-- name: UpdateSessionExpiryTime :exec
UPDATE sessions SET expires_at = $2 WHERE token = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP;


-- CATEGORIES --

-- name: FindCategoryById :one
SELECT * FROM categories WHERE id = $1;

-- name: ListCategories :many
SELECT * FROM categories ORDER BY id;

-- name: DeleteCategory :exec
DELETE FROM categories WHERE id = $1;

-- name: CreateCategory :one
INSERT INTO categories (name, description) VALUES ($1, $2) RETURNING id;

-- name: UpdateCategory :exec
UPDATE categories SET name = $1, description = $2 WHERE id = $3;

-- PRODUCTS --

-- name: FindEnabledProductsByCategory :many
SELECT * FROM products WHERE category_id = $1 AND enabled = TRUE;

-- name: ListProducts :many
SELECT * FROM products ORDER BY id;

-- name: SearchProduct :many
SELECT products.id as id, products.name, products.description, products.category_id, products.extension, products.enabled, products.pricing, products.settings, products.stock, products.stock_control, categories.name AS category_name FROM products INNER JOIN categories ON categories.id = products.category_id  WHERE (category_id = $1 OR $1 < 1) ORDER BY products.id;

-- name: ListEnabledProducts :many
SELECT * FROM products WHERE enabled ORDER BY id;

-- name: FindProductById :one
SELECT * FROM products WHERE id = $1;

-- name: FindProductsByCategory :many
SELECT * FROM products WHERE category_id = $1 ORDER BY id;

-- name: UpdateProduct :exec
UPDATE products SET name = $1, description = $2, category_id = $3, extension = $4, enabled = $5, pricing = $6, settings = $7, stock = $8, stock_control = $9 WHERE id = $10;

-- name: CreateProduct :one
INSERT INTO products (name, description, category_id, extension, enabled, pricing, settings, stock, stock_control) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id;

-- name: DeleteProduct :exec
DELETE FROM products WHERE id = $1;

-- name: FindProductOptionsByProduct :many
SELECT * FROM product_options WHERE product_id = $1;

-- name: DeleteProductOptionsByProduct :exec
DELETE FROM product_options WHERE product_id = $1;

-- name: CreateProductOption :exec
INSERT INTO product_options (product_id, name, display_name, type, regex, values, description) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- INVOICES --

-- name: CountUnpaidInvoiceForService :one
SELECT COUNT(*) FROM invoices INNER JOIN invoice_items ON invoices.id = invoice_items.invoice_id WHERE invoice_items.item_id = $1 AND invoice_items.type = 'service' AND invoices.status = 'UNPAID';

-- name: SearchInvoicesPaged :many
SELECT * FROM invoices WHERE (@status::text = '' OR @status::text = status) AND (@user_id::integer = 0 OR @user_id::integer = user_id) ORDER BY id DESC LIMIT $1 OFFSET $2;

-- name: SearchInvoicesCount :one
SELECT COUNT(*) FROM invoices WHERE (@status::text = '' OR @status::text = status) AND (@user_id::integer = 0 OR @user_id::integer = user_id);

-- name: FindInvoiceById :one
SELECT * FROM invoices WHERE id = $1;

-- name: FindInvoiceByIdWithUsername :one
SELECT invoices.*, users.name AS username FROM invoices INNER JOIN users ON invoices.user_id = users.id WHERE invoices.id = $1;

-- name: SelectInvoiceForUpdate :one
SELECT * FROM invoices WHERE id = $1 FOR UPDATE;

-- name: UpdateInvoice :exec
UPDATE invoices SET status = $1, cancellation_reason = $2, paid_at = $3, due_at = $4 WHERE id = $5;

-- name: UpdateInvoiceAmount :exec
UPDATE invoices SET amount = (SELECT SUM(amount) FROM invoice_items WHERE invoice_id = $1) WHERE id = $1;

-- name: UpdateInvoicePaid :exec
UPDATE invoices SET status = 'PAID', paid_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: CreateInvoice :one
INSERT INTO invoices (user_id, status, cancellation_reason, paid_at, due_at, amount) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id;

-- name: ListInvoiceItems :many
SELECT * FROM invoice_items WHERE invoice_id = $1 ORDER BY id;

-- name: CreateInvoiceItem :exec
INSERT INTO invoice_items (invoice_id, description, amount, type, item_id) VALUES ($1, $2, $3, $4, $5);

-- name: DeleteInvoiceItem :exec
DELETE FROM invoice_items WHERE id = $1 AND invoice_id = $2;

-- name: DeleteAllInvoiceItems :exec
DELETE FROM invoice_items WHERE invoice_id = $1;

-- name: UpdateInvoiceItem :exec
UPDATE invoice_items SET description = $1, amount = $2 WHERE id = $3 AND invoice_id = $4;

-- name: FindInvoiceByService :many
SELECT invoices.* FROM invoices INNER JOIN invoice_items ON invoices.id = invoice_items.invoice_id WHERE invoice_items.item_id = $1 AND invoice_items.type = 'service' ORDER BY invoices.id DESC;

-- name: AddInvoicePayment :one
INSERT INTO invoice_payments (invoice_id, description, amount, reference_id, gateway) VALUES ($1, $2, $3, $4, $5) RETURNING id;

-- name: ListInvoicePayments :many
SELECT * FROM invoice_payments WHERE invoice_id = $1 ORDER BY id ASC;

-- name: TotalInvoicePayment :one
SELECT SUM(amount::decimal)::decimal FROM invoice_payments WHERE invoice_id = $1;

-- name: FindOverdueInvoices :many
SELECT * FROM invoices WHERE status = 'UNPAID' AND due_at <= CURRENT_TIMESTAMP ORDER BY id;

-- name: UpdateInvoiceCancelled :exec
UPDATE invoices SET status = 'CANCELLED', cancellation_reason = $1 WHERE id = $2;

-- SERVICES --

-- name: FindServiceByIdForUpdate :one
SELECT * FROM services WHERE id = $1 FOR UPDATE;

-- name: CountServicesPaged :one
SELECT COUNT(id) FROM services WHERE (@label::text = '' OR @label::text = label) AND (@server::integer = 0 OR (settings::jsonb ? 'server' AND (settings->>'server')::integer = @server::integer)) AND (@user_id::integer = 0 OR @user_id::integer = user_id) AND (@status::text = '' OR @status::text = status);

-- name: SearchServicesPaged :many
SELECT services.label, users.name, services.id, services.status, services.user_id, services.price, services.created_at, services.billing_cycle, services.expires_at FROM services INNER JOIN users ON services.user_id = users.id WHERE (@label::text = '' OR @label::text = label) AND (@server::integer = 0 OR (settings ? 'server' AND (settings->>'server')::integer = @server::integer)) AND (@user_id::integer = 0 OR @user_id::integer = user_id) AND (@status::text = '' OR @status::text = status) ORDER BY services.id DESC LIMIT $1 OFFSET $2;

-- name: FindServiceByIdWithName :one
SELECT services.*, users.name FROM services INNER JOIN users ON services.user_id = users.id WHERE services.id = $1;

-- name: FindServiceById :one
SELECT * FROM services WHERE id = $1;

-- name: CreateService :one
INSERT INTO services (label, user_id, status, billing_cycle, price, extension, settings, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id;

-- name: UpdateServiceLabel :exec
UPDATE services SET label = $1 WHERE id = $2;

-- name: UpdateServiceSettings :exec
UPDATE services SET settings = $1 WHERE id = $2;

-- name: UpdateServiceStatus :exec
UPDATE services SET status = $1 WHERE id = $2;

-- name: UpdateService :exec
UPDATE services SET label = $1, billing_cycle = $2, price = $3, expires_at = $4 WHERE id = $5;

-- name: DeleteService :exec
DELETE FROM services WHERE id = $1;

-- name: UpdateServiceExpiryTime :exec
UPDATE services SET expires_at = $2 WHERE id = $1;

-- name: CountServicesByServer :one
SELECT COUNT(id) FROM services WHERE (status = 'PENDING' OR status = 'ACTIVE' OR status = 'SUSPENDED' OR status = 'UNPAID') AND (settings::jsonb ? 'server' AND (settings->>'server')::integer = @server::integer);

-- name: UpdateServiceCancelled :exec
UPDATE services SET cancellation_reason = $1, cancelled_at = $2, status = 'CANCELLED' WHERE id = $3;

-- name: FindOverdueServices :many
SELECT * FROM services WHERE (status = 'SUSPENDED' OR status = 'ACTIVE' OR status = 'PENDING') AND expires_at <= CURRENT_TIMESTAMP ORDER BY id;

-- GATEWAYS --

-- name: ListGateways :many
SELECT * FROM gateways ORDER BY id ASC;

-- name: ListEnabledGateways :many
SELECT display_name, name FROM gateways WHERE enabled = true ORDER BY id ASC;

-- name: CreateGatewayOrIgnore :exec
INSERT INTO gateways (name, display_name, settings, enabled, fee) VALUES ($1, $1, '{}'::json, false, '0.00%') ON CONFLICT DO NOTHING;

-- name: UpdateGateway :exec
UPDATE gateways SET display_name = $1, settings = $2, enabled = $3, fee = $4 WHERE name = $5;

-- name: ListGatewayNames :many
SELECT name FROM gateways ORDER BY id ASC;

-- name: DeleteGatewayByName :exec
DELETE FROM gateways WHERE name = $1;

-- name: FindGatewayById :one
SELECT * FROM gateways WHERE id = $1;

-- name: FindGatewayByName :one
SELECT * FROM gateways WHERE name = $1;


-- SERVERS --

-- name: ListServers :many
SELECT * FROM servers ORDER BY id ASC;

-- name: CreateServer :one
INSERT INTO servers (label, extension, settings) VALUES ($1, $2, $3) RETURNING id;

-- name: UpdateServer :exec
UPDATE servers SET label = $1, settings = $2, extension = $3 WHERE id = $4;

-- name: DeleteServer :exec
DELETE FROM servers WHERE id = $1;

-- name: FindServerById :one
SELECT * FROM servers WHERE id = $1;
