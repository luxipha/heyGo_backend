<div align="center">
  <h1>Uber Clone - Microservices Architecture</h1>
  <p>A production-ready ride-booking application with Go microservices</p>
</div>

<!-- TABLE OF CONTENTS -->
<details>
  <summary>Table of Contents</summary>
  <ol>
    <li><a href="#about-the-project">About The Project</a></li>
    <li><a href="#built-with">Built With</a></li>
    <li><a href="#architecture">Architecture</a></li>
    <li><a href="#getting-started">Getting Started</a></li>
    <li><a href="#usage">Usage</a></li>
    <li><a href="#roadmap">Roadmap</a></li>
    <li><a href="#contributing">Contributing</a></li>
  </ol>
</details>



<!-- ABOUT THE PROJECT -->
## About The Project

A production-ready ride-sharing application built with Go microservices, featuring real-time tracking, payment processing, and comprehensive observability.

**Key Features:**
* Real-time trip tracking with WebSocket updates
* Driver assignment and matching system
* Stripe payment integration
* Distributed tracing with Jaeger and OpenTelemetry
* Apache Kafka (KRaft mode) for event-driven messaging
* Kubernetes deployment with Minikube support

<p align="right">(<a href="#readme-top">back to top</a>)</p>



### Built With

* **Backend:** Go 1.24+ with gRPC and HTTP (Gin)
* **Frontend:** Next.js with React and Tailwind CSS
* **Messaging:** Apache Kafka (KRaft mode)
* **Observability:** OpenTelemetry + Jaeger + Structured Logging
* **Orchestration:** Kubernetes + Helm
* **Database:** MongoDB
* **Payments:** Stripe API

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- ARCHITECTURE -->
## Architecture
[![](https://mermaid.ink/img/pako:eNqNVt9v2jAQ_lcsP21qGvGjZSEPlSpaTX1YxWDVpAmpMvZBIkicOQ6UVf3fd4mdEgcKzQOK47v7vjt_d-aVcimAhnSW5vC3gJTDXcyWiiWzlOCTMaVjHmcs1eQpB3X49Xb88J1p2LLd4d4vFWdTUJuYw-HmnYo3oM5sH34fs10Cqf7Qb6oRFL-bnZLz5c3NnmRIRgrwteJGJmXOuTa2eyP0aFAPyXIyHtWO5YaxN7-PEoNJpNrM1nOSCw3Y_QuPWLq0nBvWl4h30fIos_Bhg5n6vMIVDTgVLyNN5II4LO9L65A8wrbyJimAyAkjwlbSBHBwENisQ2vl80T4pfezapbGRW1RHckkYaloILMNi9dsvgYXtHUQv2E-lXwF-hCccQ5ZE3sNiwZ0A_O2sjSw6oPTbB_nWbT3TB3dGEiykGrLlABBtCQTNp_H-sdP8sUwK3EmkGcS2-lnAQV8rUtwVCchGSvJIc8tJ2KoMGxDjxSZKIVapZZrpovc9_2j4nF7whGPifvM8jxepp8XkW2SzAQmOVKMZXoyF6_Nwq5bunetkLxp2HfIUQR8JQts5Bqz9DJGx3K1ZtZdHAVpCc9mZStkc3s-0WZtTFukOsGnB5QeE7u6PM4gKScQsozktmF_TmuN1qidUHZJa6q9V04m2RowlrV1SnbQc5GUq33YacFL_R2faHtPzxXt0ZN1O-6iXbRW1Zu4HxeiqjTJivk6ziPTcp-U1aXD-NPgZ46a21KL061Qj6kzc38_facarzCyv1tOdGgVk7OUpCipOSxjZEI9moBKWCzwKn8tQ8yojiCBGQ3xVTC1muEV_4Z2rNByuks5DbUqwKNKFsuIhgu2znFlZo79C1Cb4L36R8rmkoav9IWGvW_-1XVn0O_1-kE3GAyHgUd3-Lnb8fu9frc_xKfbvQ6CN4_-qyJ0_KDX7Q86QTDoDAfD66ve23_1IPGQ?type=png)](https://mermaid.live/edit#pako:eNqNVt9v2jAQ_lcsP21qGvGjZSEPlSpaTX1YxWDVpAmpMvZBIkicOQ6UVf3fd4mdEgcKzQOK47v7vjt_d-aVcimAhnSW5vC3gJTDXcyWiiWzlOCTMaVjHmcs1eQpB3X49Xb88J1p2LLd4d4vFWdTUJuYw-HmnYo3oM5sH34fs10Cqf7Qb6oRFL-bnZLz5c3NnmRIRgrwteJGJmXOuTa2eyP0aFAPyXIyHtWO5YaxN7-PEoNJpNrM1nOSCw3Y_QuPWLq0nBvWl4h30fIos_Bhg5n6vMIVDTgVLyNN5II4LO9L65A8wrbyJimAyAkjwlbSBHBwENisQ2vl80T4pfezapbGRW1RHckkYaloILMNi9dsvgYXtHUQv2E-lXwF-hCccQ5ZE3sNiwZ0A_O2sjSw6oPTbB_nWbT3TB3dGEiykGrLlABBtCQTNp_H-sdP8sUwK3EmkGcS2-lnAQV8rUtwVCchGSvJIc8tJ2KoMGxDjxSZKIVapZZrpovc9_2j4nF7whGPifvM8jxepp8XkW2SzAQmOVKMZXoyF6_Nwq5bunetkLxp2HfIUQR8JQts5Bqz9DJGx3K1ZtZdHAVpCc9mZStkc3s-0WZtTFukOsGnB5QeE7u6PM4gKScQsozktmF_TmuN1qidUHZJa6q9V04m2RowlrV1SnbQc5GUq33YacFL_R2faHtPzxXt0ZN1O-6iXbRW1Zu4HxeiqjTJivk6ziPTcp-U1aXD-NPgZ46a21KL061Qj6kzc38_facarzCyv1tOdGgVk7OUpCipOSxjZEI9moBKWCzwKn8tQ8yojiCBGQ3xVTC1muEV_4Z2rNByuks5DbUqwKNKFsuIhgu2znFlZo79C1Cb4L36R8rmkoav9IWGvW_-1XVn0O_1-kE3GAyHgUd3-Lnb8fu9frc_xKfbvQ6CN4_-qyJ0_KDX7Q86QTDoDAfD66ve23_1IPGQ)


**Key Components:**
- **API Gateway**: HTTP/WebSocket entry point with request routing
- **Trip Service**: Core business logic for trip management
- **Driver Service**: Driver matching and location management  
- **Payment Service**: Stripe integration for payment processing
- **Apache Kafka**: Event streaming with KRaft mode (no ZooKeeper)
- **Jaeger**: Distributed tracing and observability

<p align="right">(<a href="#readme-top">back to top</a>)</p>



<!-- GETTING STARTED -->
## Getting Started

### Prerequisites

* **Docker Desktop** (v4.0+)
* **Minikube** (v1.30+) 
* **kubectl** (v1.27+)
* **Helm** (v3.12+)

### Installation

1. **Start Minikube**
   ```sh
   minikube start --memory=6144 --cpus=4
   ```

2. **Clone and setup**
   ```sh
   git clone https://github.com/Cprakhar/uber-clone.git
   cd uber-clone
   kubectl create namespace uber-clone
   ```

3. **Configure secrets**
   ```sh
   mv k8s/dev/secrets.template.yaml k8s/dev/secrets.yaml
   # Edit k8s/dev/secrets.yaml with your MongoDB URI and Stripe keys
   ```

4. **Deploy services**
   ```sh
   make build
   make deploy
   ```

5. **Setup observability**
   ```sh
   helm repo add jaegertracing https://jaegertracing.github.io/helm-charts
   helm install jaeger jaegertracing/jaeger --namespace uber-clone --values helm/jaeger.yaml
   ```

<p align="right">(<a href="#readme-top">back to top</a>)</p>



<!-- USAGE EXAMPLES -->
## Usage

** Start minikube tunnel (in a separate terminal):**
```sh
minikube tunnel
```

**Port-forward jaeger-query service:**
```sh
kubectl port-forward -n uber-clone svc/jaeger-query 8080:16686
```
**Start the Next.js frontend:**
```sh
docker run -e NEXT_PUBLIC_API_URL="http://$(minikube ip):30080" -e NEXT_PUBLIC_WS_URL="ws://$(minikube ip):30080/ws" -e NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY="pk_test_your_key" -p 3000:3000 uber-clone/web:latest
```

**Access services:**
* Web app: http://localhost:3000 
* Jaeger UI: http://localhost:16686

<p align="right">(<a href="#readme-top">back to top</a>)</p>



<!-- ROADMAP -->
## Roadmap

- [x] Microservices Architecture (Go + gRPC)
- [x] Real-time WebSocket Updates  
- [x] Apache Kafka Event Streaming (KRaft mode)
- [x] OpenTelemetry + Jaeger Distributed Tracing
- [x] Kubernetes Deployment
- [ ] Authentication & Authorization
- [ ] Advanced Driver Matching Algorithm
- [ ] Mobile Application
- [ ] Multi-region Deployment

<p align="right">(<a href="#readme-top">back to top</a>)</p>


<!-- CONTRIBUTING -->
## Contributing

Contributions are what make the open source community such an amazing place to learn, inspire, and create. Any contributions you make are **greatly appreciated**.

If you have a suggestion that would make this better, please fork the repo and create a pull request. You can also simply open an issue with the tag "enhancement".

1. Fork the Project
2. Create your Feature Branch (`git checkout -b feature/AmazingFeature`)
3. Commit your Changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the Branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request

<p align="right">(<a href="#readme-top">back to top</a>)</p>
