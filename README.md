# Luanti Grave Scanner (v0.2)

Prosta aplikacja w **Go** do monitorowania logu serwera Luanti i wyłapywania wpisów o śmierci gracza, np.:

```txt
2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed
```

## Co robi aplikacja

- parsuje wpisy śmierci (`dies at ... Bones placed`),
- rozpoznaje odbiór grobu wyłącznie na podstawie `collects bones:bones` i łączy je z najnowszym aktywnym grobem o tych samych współrzędnych,
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
- domyślnie pokazuje tylko aktywne groby; opcja `Pokaż także podniesione groby` włącza również historię,
- w widoku wszystkich grobów aktywne wiersze są czerwonawe, a podniesione zielonkawe i zawierają nick gracza, który odebrał grób,
- zakres czasu (radio): `dziś` (domyślnie), `tydzień`, `miesiąc`, `wszystko`,
- przełącznik motywu: `white` / `black` (light/dark).

Wybrane ustawienia (nick, zakres czasu, widoczność podniesionych grobów i motyw) zapisywane są po stronie przeglądarki w `localStorage`, dzięki czemu wracają przy kolejnej wizycie.

Przykładowa para skorelowanych wpisów:

```txt
2026-04-24 17:46:02: ACTION[Server]: Mordor dies at (229,-29031,148). Bones placed
2026-06-04 13:25:50: ACTION[Server]: Alice collects bones:bones at (229,-29031,148)
```

W tym przykładzie właścicielem grobu jest `Mordor`, a grób został podniesiony przez `Alice`. Oryginalny plik logu pozostaje otwierany wyłącznie do odczytu.

Po aktualizacji istniejące wpisy z `deaths.json` pozostają zgodne i bez informacji o podniesieniu są traktowane jako aktywne. Aby uwzględnić wpisy `collects bones:bones` zapisane wcześniej w już przeskanowanej części logu, wykonaj jednorazowo **Pełny reskan logu**. Odbuduje on listę wyłącznie z aktualnego pliku logu, więc nie zachowa wpisów dostępnych już tylko w archiwalnych logach. Zwykłe odświeżanie przyrostowe zachowuje dotychczasową historię również po wykryciu skrócenia logu i może oznaczyć wcześniejszy grób jako podniesiony na podstawie nowego wpisu.

Wpisy `digs ...` są ignorowane. Statusy podniesienia zapisane wcześniej w `deaths.json` pozostają zachowane; pełny reskan odbudowuje je wyłącznie z wpisów `collects bones:bones` w aktualnym logu.

## Mod bones w Mineclonii: brak logu po odbiorze

Mod Gravestone (`bones:bones`) w przesłanej wersji usuwa opróżniony grób przez
`core.remove_node`, bez zapisu odbioru w logu. Samo rozszerzenie parsera nie wystarczy.
W repozytorium jest [poprawka modu i instrukcja instalacji](integrations/bones/README.md),
która dodaje wpis `Collector collects bones:bones at (x,y,z)` po zabraniu całej
zawartości przez uderzenie lub okno ekwipunku. Wymaga zastosowania poprawki
na serwerze i restartu Luanti. Działa także dla publicznych grobów odbieranych
przez innego gracza. Nie da się odtworzyć wcześniejszych odbiorów bez logów.

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
