# Поля OTA и статусы

Этот файл фиксирует, какие поля `context.db -> ota_task.taskData` мы трогаем при попытке перезапустить OTA и какие статусы при этом используем.

## Что меняем при `Перезапустить OTA`

| Поле | Что делаем |
| --- | --- |
| `target_baseline_version` | Возвращаем версию назначения из живого бэкапа |
| `predict_upgrade_duration` | Копируем из бэкапа |
| `preconditions_config` | Копируем из бэкапа |
| `schedule_state` | Копируем из бэкапа, если есть |
| `expire_time` | Обновляем на актуальное значение |
| `overall_state` | Сбрасываем в `{"stage":"Download","state":"Process"}` |
| `download_state` | Сбрасываем в `{"stage":"Retrive Packages","download_type":0,"percents":0}` |
| `packages_info[*].downloaded_size` | Сбрасываем в `0` |
| `packages_info[*].flash_finish` | Сбрасываем в `false` |
| `packages_info[*].start_flash_time` | Сбрасываем в `0` |
| `packages_info[*].end_flash_time` | Сбрасываем в `0` |
| `packages_info[*].start_rollback_time` | Сбрасываем в `0` |
| `packages_info[*].end_rollback_time` | Сбрасываем в `0` |
| `flash_state` | Удаляем |

## Что делает удаление недокачанных пакетов

Функция `removeUndownloadedPackagesInDB(...)`:

- удаляет выбранные элементы из `packages_info`
- удаляет `flash_state`
- удаляет `schedule_state`
- ставит:
  - `download_state = {"download_type":0,"percents":90,"stage":"Retrive Packages"}`
  - `overall_state = {"stage":"Download","state":"Process"}`

## Статусы пакетов

На уровне одного пакета используются такие статусы:

- `pending`
- `downloading`
- `downloaded`
- `flashed`

Логика определения статуса в текущем коде:

- `flash_finish = true` -> `flashed`
- `downloaded_size >= file_size` -> `downloaded`
- `downloaded_size > 0` -> `downloading`
- иначе -> `pending`

## Статусы OTA

### `overall_state`

- `stage = Download`
- `stage = Terminate`
- `state = Process`
- `state = Idle`
- `state = Failed`

### `download_state`

- `stage = Retrive Packages`
- `stage = Complete`

### `schedule_state`

- обычно используется как дополнительное состояние планировщика
- при перезапуске OTA может копироваться из бэкапа или удаляться, если сценарий его сбрасывает

## Короткий итог

Для попытки заново запустить OTA нам важно:

- вернуть `target_baseline_version`
- восстановить структуру `packages_info`
- сбросить загрузочный прогресс
- обновить `expire_time`
- привести `overall_state` и `download_state` к рабочему стартовому виду
