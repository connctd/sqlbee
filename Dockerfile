FROM scratch
ADD sqlbee /sqlbee
ENTRYPOINT ["/sqlbee"]
