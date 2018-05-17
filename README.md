Iris
=====

Iris is an Envoy control-plane implementation for Kubernetes. Typically, Envoy service discovery on Kubernetes involves 
polling the DNS endpoints to detect pod addition and deletion. This method is slow and does not scale well when the 
Kubernetes cluster is under heavy load. 

Iris is a Kubernetes controller that also implements an Envoy Aggregated Discovery Service (ADS). In addition to the
circumvention of the DNS polling issue, this gives us the added bonus of being able to control the configuration of a 
fleet of Envoys without any down time. Things like traffic shaping, canary deploys, replays etc. can be achieved quickly
and easily by just pushing a new configuration to the Iris server (think of it as a very poor man's Istio). 

Iris is configured using a YAML file which embeds standard Envoy V2 configuration objects and adds a few extra configuration options
to tailor it for the Kubernetes environment. If you're familiar with Envoy configuration files, the Iris configuration file would
be a doddle to understand. 

[Read the configuration documentation](support/docs/configuration.md)


Development
-----------

[Read the development documentation](support/docs/development.md)


Disclaimer
----------

This is an experimental project and probably not suitable for production use. 

