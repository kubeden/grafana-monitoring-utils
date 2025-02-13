# Grafana utils written in Go

The repo includes some system monitoring endpoints to monitor from Grafana and a reporting program to screenshot panels and send emails with it.

- `/src/directory-files` : get the count of directory files in a dir
- `/src/disk-space` : get the disk space on the machine
- `/src/reporting` : send screenshots of panels to an email
- `/src/certs` : monitor certificates expiration of endpoints

## Usage

To monitor files count:

```bash
# grafana; json
curl http://localhost:8080/simple?duration={timerange}
```

To monitor disk space:

```bash
# grafana; json
curl http://localhost:8080/grafana?path=/&from=1739445269690&to=1739447069690

# prometheus
curl http://localhost:8080/prometheus
```