# DNS-IPSet server

## О проекте

Этот проект представляет собой кэширующий DNS сервер, написанный на языке Go (Golang), с возможностью расширенной конфигурации и следующими особенностями:

- **Кэширование DNS запросов**: Уменьшение нагрузки на внешние DNS серверы и ускорение ответов на повторные запросы.
- **Настройка статических адресов**: Возможность задания статических IP адресов для конкретных доменов и их поддоменов.
- **Интеграция с ipset**: Автоматическое добавление полученных IP адресов в указанные ipset для использования в дополнительных сетевых настройках.

## Основные функции

- **Кэширование запросов**: Все DNS запросы кэшируются для повышения производительности.
- **Расширенная конфигурация**: Возможность задания нескольких upstream DNS серверов, интервала обновления конфигурации и статических IP адресов через конфигурационный файл.

## Пример конфигурации

```yaml
debug: false
nameservers:
  - 8.8.8.8:53
  - 8.8.4.4:53
  - 1.1.1.1:53
configUpdate: true
updateInterval: 1m
address:
  .local: 127.0.0.1
ipsets:
  vpn:
    - docker.com
    - graylog.org
```

## Установка и запуск

### Fast install
```shell
curl -o install.sh -z install.sh -L https://github.com/xMlex/dns-ipset/raw/refs/heads/main/install.sh; bash install.sh
```


### Source code run

Для запуска проекта следуйте инструкциям:


1. Клонируйте репозиторий:
   ```bash
   git clone https://github.com/xMlex/dns-ipset.git
   cd dns-ipset
   ```

2. Установите зависимости и скомпилируйте проект:
   ```bash
   go build
   ```

3. Настройте конфигурационный файл `config.yaml` в соответствии с вашими требованиями.

4. Запустите сервер:
   ```bash
   ./dns-ipset -config config.yaml
   ```

## Требования

- Go (Golang) - при локальной сборке
- ipset, iptables, iproute2

### После установки

* в интерфейсе интернет соединения установить DNS-сервер: 127.0.0.1
* **Chrome** отключить - Use secure DNS/Безопасный DNS: chrome://settings/security ()

### Arch linux

```shell
mkdir /etc/systemd/resolved.conf.d
nano /etc/systemd/resolved.conf.d/dns_servers.conf
```

set:
```shell
[Resolve]
DNS=127.0.0.1
Domains=~.
# FallbackDNS=8.8.8.8 # FallbackDNS вшит в dns-ipset
```

```shell
systemctl restart systemd-resolved
resolvectl query ya.ru
```


## TODO

* Рассмотреть вариант работы с IpSET через этот модуль: https://github.com/nadoo/ipset/blob/master/README.md