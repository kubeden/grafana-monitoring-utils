# Grafana monitoring endpoints

Some system monitoring endpoints to monitor from Grafana.

- `/src/directory-files` : get the count of directory files in a dir
- `/src/disk-space` : get the disk space on the machine

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