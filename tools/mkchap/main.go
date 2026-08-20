package main
import ("os";"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture")
func main(){
 os.WriteFile(os.Args[1], fixture.MatroskaWithChapters([]fixture.Chapter{
  {Start:0,End:5,Title:"before"},{Start:50,End:70,Title:"straddles-start"},{Start:70,End:120,Title:"straddles-end"},
 }), 0o644)
}
