# Luanti Grave Scanner (v0.2)

Prosta aplikacja w **Go** do monitorowania logu serwera Luanti i wyłapywania wpisów o śmierci gracza, np.:

```txt
2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed
```

## Co robi aplikacja

- parsuje wpisy śmierci (`dies at ... Bones placed`),
- trzyma listę znalezionych zgonów w osobnym pliku `deaths.json`,
- zapamiętuje offset odczytu (`scanner-state.json`) i odświeża przyrostowo,
- wykrywa przycięcie/rotację logu (gdy rozmiar pliku jest mniejszy niż zapisany offset) i resetuje offset,
- udostępnia API + prostą stronę HTML,
- **nie skanuje okresowo** — odświeżenie wywołujesz ręcznie przez API lub przyciski w UI.
- **nigdy nie czyści i nie modyfikuje oryginalnego `debug.txt`**; operacje czyszczenia/odbudowy dotyczą wyłącznie lokalnych danych aplikacji (`deaths.json`, `scanner-state.json`).

## API

### Odczyt danych

- `GET /api/deaths` — lista zgonów (JSON, najnowsze na początku).
- `GET /api/version` — wersja aplikacji.
- `GET /healthz` — healthcheck.

### Odświeżanie backendu

- `POST /api/refresh/incremental` — odświeżenie od ostatniego offsetu.
- `POST /api/refresh/full` — pełny skan od początku logu i odbudowa listy zgonów od zera.

## Nazwy przycisków w UI

W wersji v0.2 użyte zostały nazwy:
- **Odśwież nowe wpisy**
- **Pełny reskan logu**

## Filtry i preferencje w UI

UI oferuje:
- filtr po nicku (dropdown),
- zakres czasu (radio): `dziś` (domyślnie), `tydzień`, `miesiąc`, `wszystko`,
- przełącznik motywu: `white` / `black` (light/dark).

Wybrane ustawienia (nick, zakres czasu, motyw) zapisywane są po stronie przeglądarki w `localStorage`, dzięki czemu wracają przy kolejnej wizycie.

## Wymagania

- Go 1.22+

## Konfiguracja

| Zmienna | Wymagana | Domyślnie | Opis |
|---|---|---|---|
| `LOG_FILE_PATH` | ✅ | - | Ścieżka do pliku logu Luanti |
| `DATA_DIR` | ❌ | `./data` | Katalog na dane aplikacji (`scanner-state.json`, `deaths.json`) |
| `HTTP_ADDR` | ❌ | `:8080` | Adres HTTP aplikacji |

## Uruchomienie lokalne

```bash
export LOG_FILE_PATH=/ścieżka/do/debug.txt
go run .
```

- UI: `http://localhost:8080/`
- API deaths: `http://localhost:8080/api/deaths`

## Testy

Uruchom wszystkie testy:

```bash
go test ./...
```

Sprawdzenie builda:

```bash
go build ./...
```

## Docker

Build obrazu:

```bash
docker build -t luanti-grave-scanner:latest .
```

Przykładowe uruchomienie:

```bash
docker run -d \
  --name luanti-grave-scanner \
  -p 8080:8080 \
  -e LOG_FILE_PATH=/logs/debug.txt \
  -v /ścieżka/na/NAS/logs:/logs:ro \
  -v /ścieżka/na/NAS/luanti-grave-scanner-data:/data \
  luanti-grave-scanner:latest
```
