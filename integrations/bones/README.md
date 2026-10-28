# Logowanie odbioru grobów w modzie bones

Poprawka jest przeznaczona dla przesłanej wersji modu **Gravestone / bones**
(Mineclonia, `bones:bones`, autor Sandro del Toro). Ten mod odbiera przedmioty
przez `on_punch` lub `on_metadata_inventory_take`, a następnie usuwa grób przez
`core.remove_node`. W tych ścieżkach nie zapisuje informacji o odbiorze.

Plik [collection-logging.patch](collection-logging.patch) dodaje log:

```text
2026-10-28 20:00:00: ACTION[Server]: Collector collects bones:bones at (229,-29031,148)
```

Wpis powstaje po usunięciu opróżnionego grobu. Częściowy odbiór przedmiotów,
brak miejsca w ekwipunku i odrzucone uderzenie nie oznaczają zebrania grobu.
Nick wskazuje gracza odbierającego przedmioty, także gdy stary grób stał się
publiczny i mod wyczyścił pole `owner`. Usuwanie waypointów pozostaje zachowane.

Poprawka aktualizuje też `pos_string` po wyborze miejsca grobu. Dzięki temu
`Bones placed` i `collects bones:bones` wskazują tę samą pozycję, gdy grób
powstał w sąsiednim wolnym bloku.

## Instalacja na serwerze

Zaktualizuj skaner do wersji kodu obsługującej `collects bones:bones`.
Sam skaner nie instaluje poprawki w Luanti.

1. Skopiuj `collection-logging.patch` na serwer, np. do `/tmp/collection-logging.patch`.
2. Zatrzymaj serwer Luanti i przejdź do katalogu modu:

   ```bash
   cd ~/docker/services/luanti/data/mods/bones
   patch --dry-run --fuzz=0 -p1 < /tmp/collection-logging.patch
   ```

3. Jeśli próba zakończyła się poprawnie, zastosuj poprawkę z kopią zapasową:

   ```bash
   patch --backup --version-control=numbered --fuzz=0 -p1 < /tmp/collection-logging.patch
   ```

   Poprawka zmienia tylko `functions.lua`. Nie trzeba edytować `node.lua` ani
   `init.lua`. Nie stosuj jej ponownie, jeśli jest już zainstalowana.

4. W konfiguracji używanej przez serwer ustaw `debug_log_level = action`
   (lub pozostaw bardziej szczegółowy poziom `info` / `verbose`). Logowanie do
   pliku musi być włączone, a skaner musi czytać ten sam plik. Poziomy
   `error`, `warning` i wyłączone logowanie nie zapiszą wpisu `ACTION`.
5. Uruchom ponownie Luanti. Utwórz nowy grób i odbierz całą zawartość uderzeniem,
   a następnie sprawdź drugi grób przez okno ekwipunku. W obu przypadkach
   oczekuj wpisu `collects bones:bones`. W skanerze kliknij **Odśwież nowe wpisy**.

Pełny reskan nie odtworzy wcześniejszych odbiorów, dla których log nigdy nie
powstał. Stare groby umieszczone obok miejsca śmierci mogą nie pasować do
zapisanych dawniej współrzędnych; poprawka pozycji działa dla nowych grobów.
Aktualizacja zewnętrznego modu może nadpisać tę zmianę — wtedy porównaj nową
wersję i ponownie sprawdź logowanie.

API logowania: [dokumentacja `core.log`](https://api.luanti.org/core-namespace-reference/#logging).
Konfiguracja pliku logu: [kod Luanti, `init_log_streams`](https://github.com/luanti-org/luanti/blob/master/src/main.cpp).
Poprawka dotyczy zewnętrznego modu na licencji AGPL-3.0-or-later; zachowaj jego
informacje o autorze i licencji.

## Test callbacków bez serwera

Po zastosowaniu poprawki do kopii przesłanego `functions.lua`, z katalogu repozytorium:

```bash
lua integrations/bones/test_collection.lua /ścieżka/do/bones/functions.lua
```

Test używa atrap API Luanti. Sprawdza oba sposoby odbioru, brak miejsca,
kontrolę właściciela, publiczny grób, usuwanie waypointów i pozycję grobu
przeniesionego do sąsiedniego bloku. Nie zastępuje testu na działającym serwerze.
