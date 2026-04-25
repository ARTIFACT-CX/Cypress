"""
AREA: worker · AUDIO

Feature: binary audio frame transport between the Go server and the
worker. Owns the unix-domain socket and (eventually) the routing of
frames into and out of the model session.

Public surface:
- start_server(path): boot the UDS listener

Implementation lives in socket.py.
"""

from .socket import start_server

__all__ = ["start_server"]
