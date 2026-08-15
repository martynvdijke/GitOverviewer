package deploy

import "testing"

func TestFirstPublishedPort(t *testing.T) {
	tests := []struct {
		name string
		c    containerInspect
		want int
	}{
		{
			name: "single tcp binding",
			c: containerInspect{
				HostConfig: containerHostConfig{
					PortBindings: map[string][]containerPortBinding{
						"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
					},
				},
			},
			want: 8080,
		},
		{
			name: "udp binding ignored, tcp picked",
			c: containerInspect{
				HostConfig: containerHostConfig{
					PortBindings: map[string][]containerPortBinding{
						"53/udp": {{HostIP: "0.0.0.0", HostPort: "53"}},
					},
				},
			},
			want: 53,
		},
		{
			name: "no published ports",
			c: containerInspect{
				HostConfig: containerHostConfig{PortBindings: nil},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := portForContainer(tt.c); got != tt.want {
				t.Fatalf("portForContainer = %d, want %d", got, tt.want)
			}
		})
	}
}
