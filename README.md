# Luanti Grave Scanner v0.3

Ręcznie uruchamiany skaner logów Luanti i zapisanej mapy SQLite. Łączy historię
utworzenia/odbioru grobów z obecnością rzeczywistych node’ów `bones:bones`.

## Cztery operacje

| Przycisk | Działanie |
|---|---|
| **Odśwież nowe wpisy** | Czyta nowe, kompletne wiersze logu od zapamiętanego miejsca. |
| **Pełny reskan logu** | Ponownie czyta bieżący log i scala wyniki z historią. Niczego nie kasuje. |
| **Zweryfikuj znane groby w mapie** | Najpierw odświeża log, następnie sprawdza znane współrzędne, również wcześniejszych grobów. |
| **Znajdź wszystkie groby w mapie** | Najpierw odświeża log, następnie przegląda wszystkie zapisane bloki. Odkrywa stare groby spoza dostępnego logu i weryfikuje znane. |

Nie ma skanowania okresowego ani skanu przy uruchomieniu aplikacji lub otwarciu
strony. Strona odczytuje postęp tylko podczas uruchomionej operacji. Jednocześnie
może trwać jedna operacja; druga otrzyma HTTP 409. Skan mapy można anulować.

## Widoki i znaczenie danych

- **Istniejące — potwierdzone w mapie** (domyślny): groby potwierdzone podczas ostatniego sprawdzenia ich pozycji.
- **Do weryfikacji**: groby rozpoznane w logu, jeszcze bez potwierdzenia mapą.
- **Historia**: wszystkie rekordy, również podniesione i zastąpione przez kolejny grób.

Tabela pokazuje nick zmarłego, współrzędne, datę i jej źródło, stan, źródła danych
oraz ostatnie sprawdzenie mapy. Szczegóły zawierają odbierającego, czas odbioru,
surowy wpis logu i dostępne metadane grobu. Filtry obejmują nick i zakres czasu
(domyślnie wszystko). Preferencje widoku, filtrów, sortowania i motywu są
zapisywane w przeglądarce.

Data grobu pochodzi z logu. Jeśli jej brak, używana jest **data pierwszego
wykrycia w mapie**, oznaczona w tabeli. Kolejne skany jej nie zmieniają. Później
odnaleziony wpis utworzenia uzupełnia datę i zachowuje identyfikator rekordu.
Sortowanie domyślnie jest malejące po tej dacie; dostępne jest też „ostatnio odkryte”.

Nick ze świeżego grobu pochodzi z `owner`. Stary publiczny grób może nie mieć
tego pola: nick odzyskujemy z `infotext`, także z oryginalnymi znacznikami tłumaczeń
Luanti (`ESC(T@bones)`, `ESC F`, `ESC E`). Pusty `owner` nie kasuje wcześniej
ustalonego nicku. Brakujące metadane są uzupełniane przy ponownych skanach.
`time` w metadanych i timestamp bloku mapy nie są datami utworzenia grobu.

Brak node’a grobu oznacza **Podniesiony — brak stwierdzony skanem**. Data to czas
stwierdzenia braku, odbierający pozostaje nieustalony. Wpis `collects bones:bones`
może później dostarczyć rzeczywisty czas i nick odbierającego. Sam brak w mapie
nie rozróżnia odbioru od usunięcia administracyjnego lub zastąpienia node’a.

Historia rozróżnia kolejne groby pod tymi samymi współrzędnymi. Nowe postawienie
zamyka wcześniejszy rekord w tym miejscu. Wcześniejszy odbiór z logu lub inny
nick odczytany z mapy pozwala rozpoznać nowy grób. Jeśli między skanami grób
zostanie podniesiony i zastąpiony grobem tego samego gracza, bez obu zdarzeń
w logu, metadane nie pozwalają odróżnić tych dwóch cykli.

## Bezpieczeństwo i zakres odczytu mapy

- `debug.txt` jest otwierany tylko do odczytu.
- `map.sqlite` jest otwierany przez SQLite z `mode=ro` i `query_only=1`.
- Aplikacja nie kopiuje bazy, nie wykonuje checkpointów, migracji ani zmian jej schematu/journal mode.
- Dla dodatkowej ochrony montuj **cały katalog świata jako `:ro`**. Przy WAL SQLite musi widzieć też istniejące `map.sqlite-wal` i `map.sqlite-shm`. Nie używamy `immutable=1` dla działającej bazy.
- Obsługiwany jest wyłącznie schemat `blocks(x,y,z,data)`, rozmiar bloku 16 i format serializacji **29 (Zstd)**, zgodny z przebadanym światem Luanti 5.18.0. Sama wersja silnika nie gwarantuje, że każdy historyczny blok ma ten format.
- Nieobsługiwany/uszkodzony blok, błąd SQLite lub anulowanie skanu nie oznacza nieobecności grobu. Wyniki mapy są publikowane dopiero po poprawnym zakończeniu całego zadania. Poprzednie wyniki pozostają zachowane; wcześniejsze poprawne odświeżenie logu pozostaje zapisane.
- Pełny skan pobiera małe partie i zwalnia odczyt przed dekompresją, ograniczając blokowanie zapisu przy `journal_mode=delete`. Nie utrzymuje jednej transakcji na cały świat. Poszczególne sprawdzenia mają własny czas obserwacji.
- Uwzględniamy stan zapisany na dysku. Świeże zmiany pozostające w pamięci Luanti mogą jeszcze nie być widoczne.
- Światy z dodatkowym `readonly_backend` wymagają odczytu drugiej bazy i są odrzucane zamiast dawać niepełną weryfikację.

## Rotacja logów

Kursor przechowuje offset **następnego nieprzeczytanego wiersza**, datę ostatniego
wiersza z datą oraz SHA-256 maksymalnie 4096 bajtów przed offsetem. Przy odświeżeniu
sprawdzamy długość pliku i zgodność zapisanej zawartości. Skrócenie lub zmiana
zawartości powoduje odczyt od początku; deduplikacja zachowuje historię.
To obejmuje nowy log mniejszy, równy lub większy niż stary offset, również gdy
wpisy mają tę samą datę i sekundę. Niekompletny końcowy wiersz czeka na następny
odczyt. Wyniki i kursor są zapisywane razem.

Skaner **nie doczytuje archiwów** po rotacji. Wpisów, które między ręcznymi
skanami znalazły się wyłącznie w archiwum, nie odzyska z bieżącego pliku.

## Dane v0.3 i migracja

Jedyny aktualny magazyn to `DATA_DIR/grave-store-v3.json` (`schema_version: 3`).
Zawiera historię grobów, deduplikowane zdarzenia z logów, kursor oraz podsumowanie
ostatniego udanego skanu mapy. Zapis następuje przez plik tymczasowy i atomową podmianę.

Przy starcie:

1. Jeśli nowy magazyn istnieje, jest wczytywany. Błędny plik zgłasza błąd; aplikacja nie zastępuje go pustą historią.
2. Jeśli nowego pliku brak, aplikacja importuje dotychczasowe `deaths.json`, zachowując historię i odbierających. Stare `deaths.json` i `scanner-state.json` pozostają niezmienione.
3. Jeśli brak starych danych, powstaje pusty magazyn.

Stary offset nie ma skrótu zawartości, więc pierwszy ręczny odczyt po migracji
bezpiecznie czyta bieżący log od początku i scala duplikaty. Sama migracja
nie uruchamia skanu logu ani mapy. Nowy plik ma pierwszeństwo przy każdym
kolejnym uruchomieniu. Nie uruchamiaj równocześnie dwóch instancji zapisujących
ten sam `DATA_DIR`; przed aktualizacją zatrzymaj kontener v0.2.

## Konfiguracja

| Zmienna | Domyślnie | Opis |
|---|---|---|
| `LOG_FILE_PATH` | wymagana | Ścieżka do bieżącego logu Luanti. |
| `MAP_FILE_PATH` | opcjonalna | Ścieżka do oryginalnego `map.sqlite`. Bez niej oba przyciski mapy są nieaktywne. |
| `DATA_DIR` | `./data` | Katalog danych aplikacji. |
| `HTTP_ADDR` | `:8080` | Adres HTTP. |
| `LOG_TIMEZONE` | lokalna strefa procesu | Strefa dat w logu, np. `Europe/Warsaw`. Ustaw zgodnie z konfiguracją serwera Luanti i zachowaj ją między uruchomieniami. |

## Budowanie i uruchomienie

Jedynym źródłem wersji aplikacji i polecenia tagowania jest plik **`VERSION`**,
obecnie `v0.3`. Dockerfile nie może sam nadać tagu wynikowemu obrazowi — tag jest
argumentem polecenia budowania. Skrypt odczytuje `VERSION`, nadaje tag i przekazuje
wersję do etykiety OCI obrazu:

```sh
./scripts/build-image.sh
# równoważnie:
make image
# wynik: luanti-grave-scanner:v0.3
```

Budowanie nie publikuje obrazu w rejestrze. Inną nazwę repozytorium obrazu można
podać przez `IMAGE_NAME=twoja-nazwa ./scripts/build-image.sh`. Dodatkowe argumenty
Docker, np. `--no-cache`, są przekazywane przez skrypt. Sama aplikacja i UI
odczytują wersję osadzoną z `VERSION`.

```sh
docker run -d \
  --name luanti-grave-scanner \
  -p 8080:8080 \
  -e LOG_FILE_PATH=/logs/debug.txt \
  -e MAP_FILE_PATH=/world/map.sqlite \
  -e LOG_TIMEZONE=Europe/Warsaw \
  -v /ścieżka/na/NAS/logs:/logs:ro \
  -v /ścieżka/na/NAS/świat:/world:ro \
  -v /ścieżka/na/NAS/luanti-grave-scanner-data:/data \
  luanti-grave-scanner:v0.3
```

UI: `http://localhost:8080/`. Obraz uruchamia aplikację jako użytkownik bez roota;
źródła muszą być dla niego czytelne, a `/data` zapisywalne. Przy aktualizacji użyj
dotychczasowego wolumenu `/data`, aby wykonać import historii.

Lokalnie wymagany jest Go **1.26+** (zależności SQLite/Zstd). Docker dostarcza
kompilator, więc do budowy obrazu Go na hoście nie jest potrzebny:

```sh
LOG_FILE_PATH=/ścieżka/debug.txt MAP_FILE_PATH=/ścieżka/map.sqlite go run .
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

## API

| Metoda i ścieżka | Działanie |
|---|---|
| `GET /api/deaths` | Wszystkie groby, najnowsze na początku; zachowane pola wcześniejszego API, uzupełnione o stan i źródła. |
| `GET /api/status` | Wersja, dostępność mapy, liczniki, postęp zadania i ostatni udany skan mapy. |
| `GET /api/version` | Wersja z `VERSION`. |
| `POST /api/refresh/incremental` | Synchroniczny przyrostowy odczyt logu. |
| `POST /api/refresh/full` | Synchroniczny odczyt całego bieżącego logu i scalenie historii. `added` oznacza nowe zdarzenia utworzenia, nie całkowitą liczbę odczytanych wpisów. |
| `POST /api/map/verify` | Uruchomienie zadania: log, potem znane współrzędne; HTTP 202. |
| `POST /api/map/full` | Uruchomienie zadania: log, potem cała mapa; HTTP 202. |
| `POST /api/scan/cancel` | Anulowanie bieżącego zadania mapy; JSON `{"id":"id-zadania"}`. |
| `GET /healthz` | Healthcheck procesu. |

Zadanie ma stan `running`, `succeeded`, `failed` lub `cancelled`. Podczas pracy
`stage` to `logs`, `map` lub `saving`; po zakończeniu `done`. GET-y nie uruchamiają
skanowania. Brak skonfigurowanej mapy daje HTTP 400, konflikt operacji HTTP 409.

## Odbiór grobu a mod bones

Obsługujemy wpisy:

```text
2026-04-24 17:46:02: ACTION[Server]: Mordor dies at (229,-29031,148). Bones placed
2026-06-04 13:25:50: ACTION[Server]: Alice collects bones:bones at (229,-29031,148)
```

`digs ...` nie potwierdza odbioru i jest ignorowane. Mod Gravestone / bones
w używanej wersji wymaga poprawki, aby po opróżnieniu grobu zapisywał
`collects bones:bones`. Zachowano [poprawkę i instrukcję instalacji](integrations/bones/README.md).
Bez dawnych logów nie ustalimy odbierającego; odczyt mapy potwierdzi brak node’a.

## Weryfikacja implementacji

Testy obejmują metadane świeżego grobu Miki i publicznego grobu Mordora,
ujemne współrzędne, `air`, uzupełnianie metadanych i późniejsze scalenie logu,
ponowne użycie współrzędnych, migrację i restart, deduplikację, trzy rozmiary
logu po rotacji, niekompletny wiersz, nieudany zapis kursora, odczyt WAL,
read-only, stronicowanie pełnego skanu, nieobsługiwany blok i anulowanie.
