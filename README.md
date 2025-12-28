# billing3


## Deployment

Docker compose

```yaml
services:
  db:
    image: postgres:18
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: postgres
    volumes:
      - /srv/billing3db:/var/lib/postgresql

  backend:
    image: ghcr.io/billing3dev/billing3:master
    depends_on:
      - db
    environment:
      SMTP_USERNAME: 
      SMTP_PASSWORD: 
      SMTP_ENDPOINT: smtp.tem.scaleway.com
      SMTP_PORT: 2465
      SMTP_TLS: TLS
      SMTP_FROM: mailer@example.com
      JWT_KEY: 74fd6ebcbf1a9095d7b360543ae0285a # Replace with your own key, hex encoded.
      DATABASE: postgres://postgres:postgres@db:5432/postgres
      DEBUG: false
      PUBLIC_DOMAIN: https://billing3.example.com


  frontend:
    image: ghcr.io/billing3dev/billing3-frontend:master
    depends_on:
      - backend
    ports:
      - "127.0.0.1:6000:80"

```

River migration

```shell
# Start a golang container and connect to the docker compose network
docker run --rm -it --network billing3_default golang:1.25-trixie /bin/bash

# Execute the following commands inside the container
go install github.com/riverqueue/river/cmd/river@latest
river migrate-up --line main --database-url "postgres://postgres:postgres@db:5432/postgres"
```

## Documentation

See [docs](./docs).

## Development

### Setup

1. Install golang
2. Copy and modify `.env.example` to `.env`
3. Start postgresql server
```
docker run -d --name billing3-pg -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=postgres -p 5432:5432 postgres:18
```
4. `go run .`


### SQLC Code Generation

Run the following command after modifying the SQL schema or `query.sql`:

```
sqlc generate
```
