build:
	docker build -t homelab .
	
run:
	docker run -dit --name homelab homelab
