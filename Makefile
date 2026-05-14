build:
	docker build -t homelab .
	
run:
	docker run -p 5173:5173 -dit --name homelab homelab
