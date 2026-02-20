# Luanti Grave Scanner

Prosta aplikacja w **Go** do monitorowania logu serwera Luanti i wyłapywania wpisów o śmierci gracza, np.:

```txt
2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed
```

Aplikacja:
- co określony interwał (domyślnie 5 minut) skanuje plik logu,
- zaczyna kolejne skanowanie od poprzedniego offsetu (przyrostowo),
- wykrywa przycięcie/rotację loga (gdy rozmiar pliku jest mniejszy niż zapamiętany offset) i resetuje offset,
- zapisuje znalezione zdarzenia do osobnego pliku JSON,
- udostępnia API `GET /api/deaths`,
- serwuje prosty frontend HTML z tabelą (kto, kiedy, współrzędne) pod `GET /`.

## Wymagania

- Go 1.22+

## Konfiguracja

| Zmienna | Wymagana | Domyślnie | Opis |
|---|---|---|---|
| `LOG_FILE_PATH` | ✅ | - | Ścieżka do pliku logu Luanti |
| `DATA_DIR` | ❌ | `./data` | Katalog na dane aplikacji (`scanner-state.json`, `deaths.json`) |
| `HTTP_ADDR` | ❌ | `:8080` | Adres HTTP aplikacji |
| `SCAN_INTERVAL` | ❌ | `5m` | Interwał skanowania, format `time.ParseDuration` |

## Uruchomienie lokalne

```bash
export LOG_FILE_PATH=/ścieżka/do/debug.txt
export SCAN_INTERVAL=5m
go run .
```

Następnie:
- API: `http://localhost:8080/api/deaths`
- UI: `http://localhost:8080/`

## Pliki danych

Aplikacja tworzy w `DATA_DIR`:
- `scanner-state.json` — ostatni offset odczytu logu,
- `deaths.json` — lista wykrytych zgonów.

## Docker

Przykładowy `Dockerfile`:

```Dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/luanti-grave-scanner .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/luanti-grave-scanner /usr/local/bin/luanti-grave-scanner
COPY web ./web
RUN adduser -D appuser && mkdir -p /data && chown -R appuser:appuser /data
USER appuser
ENV DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/luanti-grave-scanner"]
```

Przykładowe uruchomienie kontenera:

```bash
docker run -d \
  --name luanti-grave-scanner \
  -p 8080:8080 \
  -e LOG_FILE_PATH=/logs/debug.txt \
  -e SCAN_INTERVAL=5m \
  -v /ścieżka/na/NAS/logs:/logs:ro \
  -v /ścieżka/na/NAS/luanti-grave-scanner-data:/data \
  luanti-grave-scanner:latest
```

> W środowisku NAS wystarczy dodać ten image do Twojego projektu obok kontenera serwera Luanti oraz podmontować log serwera jako volume tylko do odczytu.
