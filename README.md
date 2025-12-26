# billing3



## Development Setup

1. Install golang
2. Copy and modify `.env.example` to `.env`
3. Start postgresql server
```
docker run -d --name billing3-pg -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=postgres -p 5432:5432 postgres:18
```
4. `go run .`


## SQLC Code Generation

Run the following command after modifying the SQL schema or `query.sql`:

```
sqlc generate
```
