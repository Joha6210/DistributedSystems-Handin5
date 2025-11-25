# Generate proto files

protoc -I=.  -I=grpc/auctionServer/auction.proto --go_out=./grpc --go-grpc_out=./grpc grpc/backe
nd/backend.proto grpc/auctionServer/auction.proto
